package obscuro

import (
	"fmt"
	"simulation/common"
)

type SubmitBlockResponse struct {
	root      common.RootHash
	rollup    common.EncodedRollup
	processed bool
}

// Enclave - The actual implementation of this interface will call an rpc service
type Enclave interface {
	// Todo - attestation, secret generation, etc

	// SubmitBlock - When a new round starts, the host submits a block to the enclave, which responds with a rollup
	// it is the responsibility of the host to gossip the rollup
	SubmitBlock(block common.EncodedBlock) SubmitBlockResponse

	Stop()
	Start()

	// SubmitRollup - receive gossiped rollups
	SubmitRollup(rollup common.EncodedRollup)

	// SubmitTx - user transactions
	SubmitTx(tx EncodedL2Tx)

	// Balance
	Balance(address common.Address) uint64

	// RoundWinner - calculates and returns the winner for a round
	RoundWinner(parent common.RootHash) (common.EncodedRollup, bool)

	// PeekHead - only availble for testing purposes
	PeekHead() BlockState

	// Db - only availble for testing purposes
	Db() Db
}

type enclaveImpl struct {
	node           common.NodeId
	mining         bool
	db             Db
	statsCollector StatsCollector

	txCh                 chan L2Tx
	roundWinnerCh        chan Rollup
	exitCh               chan bool
	speculativeWorkInCh  chan bool
	speculativeWorkOutCh chan speculativeWork
}

func (e *enclaveImpl) Start() {
	var currentHead Rollup
	var currentState RollupState
	var currentProcessedTxs []L2Tx
	var currentProcessedTxsMap = make(map[common.TxHash]L2Tx)

	//start the speculative rollup execution loop
	for {
		select {
		// A new winner was found after gossiping. Start speculatively executing incoming transactions to already have a rollup ready when the next round starts.
		case winnerRollup := <-e.roundWinnerCh:

			currentHead = winnerRollup
			currentState = newProcessedState(e.db.FetchRollupState(winnerRollup.RootHash))

			// determine the transactions that were not yet included
			currentProcessedTxs = currentTxs(winnerRollup, e.db.FetchTxs(), e.db)
			currentProcessedTxsMap = makeMap(currentProcessedTxs)

			// calculate the State after executing them
			currentState = executeTransactions(currentProcessedTxs, currentState)

		case tx := <-e.txCh:
			_, f := currentProcessedTxsMap[tx.Id]
			if !f {
				currentProcessedTxsMap[tx.Id] = tx
				currentProcessedTxs = append(currentProcessedTxs, tx)
				executeTx(&currentState, tx)
			}

		case <-e.speculativeWorkInCh:
			b := make([]L2Tx, 0)
			for _, tx := range currentProcessedTxs {
				b = append(b, tx)
			}
			e.speculativeWorkOutCh <- speculativeWork{
				r:   currentHead,
				s:   copyProcessedState(currentState),
				txs: b,
			}

		case <-e.exitCh:
			return
		}
	}
}

func (e *enclaveImpl) SubmitBlock(block common.EncodedBlock) SubmitBlockResponse {
	b := block.DecodeBlock()
	e.db.Store(b)

	_, f := e.db.Resolve(b.ParentHash)
	if !f {
		return SubmitBlockResponse{processed: false}
	}
	blockState := updateState(b, e.db)

	if e.mining {
		e.db.PruneTxs(historicTxs(blockState.Head, e.db))

		r := e.produceRollup(b, blockState)
		e.db.StoreRollup(r.Height, r)

		return SubmitBlockResponse{
			root:      blockState.Head.RootHash,
			rollup:    EncodeRollup(r),
			processed: true,
		}
	}

	return SubmitBlockResponse{
		root:      blockState.Head.RootHash,
		processed: true,
	}
}

func (e *enclaveImpl) SubmitRollup(rollup common.EncodedRollup) {
	r := DecodeRollup(rollup)
	e.db.StoreRollup(r.Height, r)
}

func (e *enclaveImpl) SubmitTx(tx EncodedL2Tx) {
	t := DecodeTx(tx)
	e.db.StoreTx(t)
	e.txCh <- t
}

func (e *enclaveImpl) RoundWinner(parent common.RootHash) (common.EncodedRollup, bool) {

	head := e.db.FetchRollup(parent)

	rollupsReceivedFromPeers := e.db.FetchRollups(head.Height + 1)
	// filter out rollups with a different Parent
	var usefulRollups []Rollup
	for _, rol := range rollupsReceivedFromPeers {
		if rol.Parent(e.db).RootHash == head.RootHash {
			usefulRollups = append(usefulRollups, rol)
		}
	}

	parentState := e.db.FetchRollupState(head.RootHash)
	// determine the winner of the round
	winnerRollup, s := findRoundWinner(usefulRollups, head, parentState, e.db)
	//common.Log(fmt.Sprintf(">   Agg%d: Round=r_%d Winner=r_%d(%d)[r_%d]{proof=b_%d}.", e.node, parent.ID(), winnerRollup.RootHash.ID(), winnerRollup.Height(), winnerRollup.Parent().RootHash.ID(), winnerRollup.Proof().RootHash.ID()))

	e.db.SetRollupState(winnerRollup.RootHash, s)
	go e.notifySpeculative(winnerRollup)

	// we are the winner
	if winnerRollup.Agg == e.node {
		v := winnerRollup.Proof(e.db)
		common.Log(fmt.Sprintf(">   Agg%d: create rollup=r_%d(%d)[r_%d]{proof=b_%d}. Txs: %v. State=%v.", e.node, winnerRollup.RootHash.ID(), winnerRollup.Height, winnerRollup.Parent(e.db).RootHash.ID(), v.RootHash.ID(), printTxs(winnerRollup.Transactions), winnerRollup.State))
		return EncodeRollup(winnerRollup), true
	}
	return nil, false
}

func (e *enclaveImpl) notifySpeculative(winnerRollup Rollup) {
	//if atomic.LoadInt32(e.interrupt) == 1 {
	//	return
	//}
	e.roundWinnerCh <- winnerRollup
}

func (e *enclaveImpl) Balance(address common.Address) uint64 {
	//todo
	return 0
}

func (e *enclaveImpl) produceRollup(b common.Block, bs BlockState) Rollup {

	// retrieve the speculatively calculated State based on the previous winner and the incoming transactions
	e.speculativeWorkInCh <- true
	speculativeRollup := <-e.speculativeWorkOutCh

	newRollupTxs := speculativeRollup.txs
	newRollupState := speculativeRollup.s

	// the speculative execution has been processing on top of the wrong parent - due to failure in gossip or publishing to L1
	//if true {
	if speculativeRollup.r.RootHash != bs.Head.RootHash {
		common.Log(fmt.Sprintf(">   Agg%d: Recalculate. speculative=r_%d(%d), published=r_%d(%d)", e.node, speculativeRollup.r.RootHash.ID(), speculativeRollup.r.Height, bs.Head.RootHash.ID(), bs.Head.Height))
		e.statsCollector.L2Recalc(e.node)

		// determine transactions to include in new rollup and process them
		newRollupTxs = currentTxs(bs.Head, e.db.FetchTxs(), e.db)
		newRollupState = executeTransactions(newRollupTxs, newProcessedState(bs.State))
	}

	// always process deposits last
	// process deposits from the proof of the parent to the current block (which is the proof of the new rollup)
	proof := bs.Head.Proof(e.db)
	newRollupState = processDeposits(&proof, b, copyProcessedState(newRollupState), e.db)

	// Create a new rollup based on the proof of inclusion of the previous, including all new transactions
	return NewRollup(&b, &bs.Head, e.node, newRollupTxs, newRollupState.w, common.GenerateNonce(), serialize(newRollupState.s))
}

func (e *enclaveImpl) PeekHead() BlockState {
	return e.db.Head()
}

func (e *enclaveImpl) Db() Db {
	return e.db
}

func (e *enclaveImpl) Stop() {
	e.exitCh <- true
}

// internal structure to pass information.
type speculativeWork struct {
	r   Rollup
	s   RollupState
	txs []L2Tx
}

func NewEnclave(id common.NodeId, mining bool, collector StatsCollector) Enclave {
	return &enclaveImpl{
		node:                 id,
		db:                   NewInMemoryDb(),
		mining:               mining,
		txCh:                 make(chan L2Tx),
		roundWinnerCh:        make(chan Rollup),
		exitCh:               make(chan bool),
		speculativeWorkInCh:  make(chan bool),
		speculativeWorkOutCh: make(chan speculativeWork),
		statsCollector:       collector,
	}
}
