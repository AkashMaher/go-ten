package networkmanager

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/obscuronet/obscuro-playground/integration/simulation/params"

	"github.com/obscuronet/obscuro-playground/integration/simulation/stats"

	"github.com/obscuronet/obscuro-playground/go/ethclient"
	"github.com/obscuronet/obscuro-playground/go/ethclient/erc20contractlib"
	"github.com/obscuronet/obscuro-playground/go/ethclient/mgmtcontractlib"
	"github.com/obscuronet/obscuro-playground/go/obscuronode/config"
	"github.com/obscuronet/obscuro-playground/go/obscuronode/obscuroclient"
	"github.com/obscuronet/obscuro-playground/go/obscuronode/wallet"
	"github.com/obscuronet/obscuro-playground/integration/simulation"
)

func InjectTransactions(nmConfig Config) {
	hostConfig := config.HostConfig{
		L1NodeHost:          nmConfig.l1NodeHost,
		L1NodeWebsocketPort: nmConfig.l1NodeWebsocketPort,
		L1ConnectionTimeout: nmConfig.l1ConnectionTimeout,
	}
	l1Client, err := ethclient.NewEthClient(hostConfig)
	if err != nil {
		panic(fmt.Sprintf("could not create L1 client. Cause: %s", err))
	}
	l2Client := obscuroclient.NewClient(nmConfig.obscuroClientAddress)

	// TODO - Handle multiple private keys and corresponding wallets.
	wallets := params.NewSimWallets(1, 1, nmConfig.ethereumChainID.Int64(), nmConfig.obscuroChainID.Int64())
	// We override the autogenerated Ethereum wallets with ones using the provided private keys.
	privateKey, err := crypto.HexToECDSA(nmConfig.privateKeyString)
	if err != nil {
		panic(fmt.Errorf("could not recover private key from hex. Cause: %w", err))
	}
	l1Wallet := wallet.NewInMemoryWalletFromPK(&nmConfig.ethereumChainID, privateKey)
	wallets.SimEthWallets = []wallet.Wallet{l1Wallet}

	txInjector := simulation.NewTransactionInjector(
		1*time.Second,
		stats.NewStats(1),
		[]ethclient.EthClient{l1Client},
		wallets,
		&nmConfig.mgmtContractAddress,
		[]obscuroclient.Client{l2Client},
		mgmtcontractlib.NewMgmtContractLib(&nmConfig.mgmtContractAddress),
		erc20contractlib.NewERC20ContractLib(&nmConfig.mgmtContractAddress, &nmConfig.erc20ContractAddress),
	)

	println("Injecting transactions into network...")
	txInjector.Start()
}
