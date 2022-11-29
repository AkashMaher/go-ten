package crosschain

import (
	"fmt"

	gethcommon "github.com/ethereum/go-ethereum/common"
	gethlog "github.com/ethereum/go-ethereum/log"
	"github.com/obscuronet/go-obscuro/go/common"
	"github.com/obscuronet/go-obscuro/go/enclave/db"
)

type blockMessageExtractor struct {
	busAddress   *common.L1Address
	l2MessageBus *common.L2Address // TODO: remove this
	storage      db.Storage
	logger       gethlog.Logger
}

func NewBlockMessageExtractor(
	busAddress *common.L1Address,
	l2BusAddress *common.L2Address,
	storage db.Storage,
	logger gethlog.Logger,
) BlockMessageExtractor {
	return &blockMessageExtractor{
		busAddress:   busAddress,
		l2MessageBus: l2BusAddress,
		storage:      storage,
		logger:       logger,
	}
}

func (m *blockMessageExtractor) Enabled() bool {
	return m.GetBusAddress().Hash().Big().Cmp(gethcommon.Big0) != 0
}

// ProcessCrossChainMessages - extracts the cross chain messages for the corresponding block from the receipts.
// The messages will be stored in DB storage for later usage.
// block - the L1 block for which events are extracted.
// receipts - all of the receipts for the corresponding block. This is validated.
func (m *blockMessageExtractor) ProcessCrossChainMessages(block *common.L1Block, receipts common.L1Receipts) error {
	areReceiptsValid := VerifyReceiptHash(block, receipts)

	if !areReceiptsValid && m.Enabled() {
		m.logger.Error("[CrossChain] Invalid receipts submitted", "block", common.ShortHash(block.Hash()))
		return fmt.Errorf("receipts do not match the receipt root for the block")
	}

	if len(receipts) == 0 {
		// Error if block receipts root does not match receipts hash
		// else nil
		return nil
	}

	lazilyLogReceiptChecksum(fmt.Sprintf("[CrossChain] Processing block: %s receipts: %d", block.Hash().Hex(), len(receipts)), receipts, m.logger)
	messages, err := m.getSyntheticTransactions(block, receipts)
	if err != nil {
		return err
	}

	if len(messages) > 0 {
		m.logger.Trace(fmt.Sprintf("[CrossChain] Storing %d messages for block %s", len(messages), block.Hash().Hex()))
		m.storage.StoreL1Messages(block.Hash(), messages)
	}

	return nil
}

// GetBusAddress - Returns the address of the L1 message bus.
func (m *blockMessageExtractor) GetBusAddress() *common.L1Address {
	return m.busAddress
}

// getSyntheticTransactions - Converts the relevant logs from the appropriate message bus address to synthetic transactions and returns them
func (m *blockMessageExtractor) getSyntheticTransactions(block *common.L1Block, receipts common.L1Receipts) (common.CrossChainMessages, error) {
	if len(receipts) == 0 {
		return make(common.CrossChainMessages, 0), nil
	}

	// Retrieves the relevant logs from the message bus.
	logs, err := filterLogsFromReceipts(receipts, m.GetBusAddress(), &CrossChainEventID)
	if err != nil {
		m.logger.Error("[CrossChain]", "Error", err)
		return make(common.CrossChainMessages, 0), err
	}
	m.logger.Trace("[CrossChain] extracted logs", "logCount", len(logs))

	messages, err := convertLogsToMessages(logs, CrossChainEventName, MessageBusABI)
	if err != nil {
		m.logger.Error("[CrossChain]", "Error", err)
		return make(common.CrossChainMessages, 0), err
	}

	m.logger.Trace(fmt.Sprintf("[CrossChain] Found %d cross chain messages that will be submitted to L2!", len(messages)),
		"Block", block.Hash().Hex())

	return messages, nil
}
