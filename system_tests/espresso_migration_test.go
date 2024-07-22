package arbtest

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/offchainlabs/nitro/arbnode"
	"github.com/offchainlabs/nitro/cmd/chaininfo"
	"github.com/offchainlabs/nitro/solgen/go/upgrade_executorgen"
	"github.com/offchainlabs/nitro/util/headerreader"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/offchainlabs/nitro/system_tests/non-espresso-nitro-contracts/go/bridgegen"
	"github.com/offchainlabs/nitro/system_tests/non-espresso-nitro-contracts/go/challengegen"
	"github.com/offchainlabs/nitro/system_tests/non-espresso-nitro-contracts/go/ospgen"
	"github.com/offchainlabs/nitro/system_tests/non-espresso-nitro-contracts/go/yulgen"

	lightclientmock "github.com/EspressoSystems/espresso-sequencer-go/light-client-mock"
	espressoTypes "github.com/EspressoSystems/espresso-sequencer-go/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/solgen/go/rollupgen"
	"github.com/offchainlabs/nitro/staker"
	"github.com/offchainlabs/nitro/validator"
)

func andTxSucceeded(ctx context.Context, l1Reader *headerreader.HeaderReader, tx *types.Transaction, err error) error {
	if err != nil {
		return fmt.Errorf("error submitting tx: %w", err)
	}
	_, err = l1Reader.WaitForTxApproval(ctx, tx)
	if err != nil {
		return fmt.Errorf("error executing tx: %w", err)
	}
	return nil
}

func deployBridgeCreator(ctx context.Context, l1Reader *headerreader.HeaderReader, auth *bind.TransactOpts, maxDataSize *big.Int, isUsingFeeToken bool) (common.Address, error) {
	client := l1Reader.Client()

	/// deploy eth based templates
	bridgeTemplate, tx, _, err := bridgegen.DeployBridge(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("bridge deploy error: %w", err)
	}

	reader4844, tx, _, err := yulgen.DeployReader4844(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("blob basefee reader deploy error: %w", err)
	}
	seqInboxTemplate, tx, _, err := bridgegen.DeploySequencerInbox(auth, client, maxDataSize, reader4844, isUsingFeeToken)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("sequencer inbox deploy error: %w", err)
	}

	inboxTemplate, tx, _, err := bridgegen.DeployInbox(auth, client, maxDataSize)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("inbox deploy error: %w", err)
	}

	rollupEventBridgeTemplate, tx, _, err := rollupgen.DeployRollupEventInbox(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("rollup event bridge deploy error: %w", err)
	}

	outboxTemplate, tx, _, err := bridgegen.DeployOutbox(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("outbox deploy error: %w", err)
	}

	ethBasedTemplates := rollupgen.BridgeCreatorBridgeContracts{
		Bridge:           bridgeTemplate,
		SequencerInbox:   seqInboxTemplate,
		Inbox:            inboxTemplate,
		RollupEventInbox: rollupEventBridgeTemplate,
		Outbox:           outboxTemplate,
	}

	/// deploy ERC20 based templates
	erc20BridgeTemplate, tx, _, err := bridgegen.DeployERC20Bridge(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("bridge deploy error: %w", err)
	}

	erc20InboxTemplate, tx, _, err := bridgegen.DeployERC20Inbox(auth, client, maxDataSize)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("inbox deploy error: %w", err)
	}

	erc20RollupEventBridgeTemplate, tx, _, err := rollupgen.DeployERC20RollupEventInbox(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("rollup event bridge deploy error: %w", err)
	}

	erc20OutboxTemplate, tx, _, err := bridgegen.DeployERC20Outbox(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("outbox deploy error: %w", err)
	}

	erc20BasedTemplates := rollupgen.BridgeCreatorBridgeContracts{
		Bridge:           erc20BridgeTemplate,
		SequencerInbox:   seqInboxTemplate,
		Inbox:            erc20InboxTemplate,
		RollupEventInbox: erc20RollupEventBridgeTemplate,
		Outbox:           erc20OutboxTemplate,
	}

	bridgeCreatorAddr, tx, _, err := rollupgen.DeployBridgeCreator(auth, client, ethBasedTemplates, erc20BasedTemplates)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, fmt.Errorf("bridge creator deploy error: %w", err)
	}

	return bridgeCreatorAddr, nil
}
func deployChallengeFactory(ctx context.Context, l1Reader *headerreader.HeaderReader, auth *bind.TransactOpts) (common.Address, common.Address, error) {
	client := l1Reader.Client()
	osp0, tx, _, err := ospgen.DeployOneStepProver0(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("osp0 deploy error: %w", err)
	}

	ospMem, tx, _, err := ospgen.DeployOneStepProverMemory(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospMemory deploy error: %w", err)
	}

	ospMath, tx, _, err := ospgen.DeployOneStepProverMath(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospMath deploy error: %w", err)
	}

	ospHostIo, tx, _, err := ospgen.DeployOneStepProverHostIo(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospHostIo deploy error: %w", err)
	}

	challengeManagerAddr, tx, _, err := challengegen.DeployChallengeManager(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("challenge manager deploy error: %w", err)
	}

	ospEntryAddr, tx, _, err := ospgen.DeployOneStepProofEntry(auth, client, osp0, ospMem, ospMath, ospHostIo)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospEntry deploy error: %w", err)
	}

	return ospEntryAddr, challengeManagerAddr, nil
}

func deployRollupCreator(ctx context.Context, l1Reader *headerreader.HeaderReader, auth *bind.TransactOpts, maxDataSize *big.Int, hotshot common.Address, isUsingFeeToken bool) (*rollupgen.RollupCreator, common.Address, common.Address, common.Address, error) {
	bridgeCreator, err := deployBridgeCreator(ctx, l1Reader, auth, maxDataSize, isUsingFeeToken)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("bridge creator deploy error: %w", err)
	}

	ospEntryAddr, challengeManagerAddr, err := deployChallengeFactory(ctx, l1Reader, auth)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, err
	}

	rollupAdminLogic, tx, _, err := rollupgen.DeployRollupAdminLogic(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("rollup admin logic deploy error: %w", err)
	}

	rollupUserLogic, tx, _, err := rollupgen.DeployRollupUserLogic(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("rollup user logic deploy error: %w", err)
	}

	rollupCreatorAddress, tx, rollupCreator, err := rollupgen.DeployRollupCreator(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("rollup creator deploy error: %w", err)
	}

	upgradeExecutor, tx, _, err := upgrade_executorgen.DeployUpgradeExecutor(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("upgrade executor deploy error: %w", err)
	}

	validatorUtils, tx, _, err := rollupgen.DeployValidatorUtils(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("validator utils deploy error: %w", err)
	}

	validatorWalletCreator, tx, _, err := rollupgen.DeployValidatorWalletCreator(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("validator wallet creator deploy error: %w", err)
	}

	l2FactoriesDeployHelper, tx, _, err := rollupgen.DeployDeployHelper(auth, l1Reader.Client())
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("deploy helper creator deploy error: %w", err)
	}

	tx, err = rollupCreator.SetTemplates(
		auth,
		bridgeCreator,
		ospEntryAddr,
		challengeManagerAddr,
		rollupAdminLogic,
		rollupUserLogic,
		upgradeExecutor,
		validatorUtils,
		validatorWalletCreator,
		l2FactoriesDeployHelper,
	)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return nil, common.Address{}, common.Address{}, common.Address{}, fmt.Errorf("rollup set template error: %w", err)
	}

	return rollupCreator, rollupCreatorAddress, validatorUtils, validatorWalletCreator, nil
}

func DeployOnL1NonEspresso(ctx context.Context, parentChainReader *headerreader.HeaderReader, deployAuth *bind.TransactOpts, batchPosters []common.Address, batchPosterManager common.Address, authorizeValidators uint64, config rollupgen.Config, nativeToken common.Address, maxDataSize *big.Int, hotshot common.Address, isUsingFeeToken bool) (*chaininfo.RollupAddresses, common.Address, common.Address, error) {
	if config.WasmModuleRoot == (common.Hash{}) {
		return nil, *new(common.Address), *new(common.Address), errors.New("no machine specified")
	}

	rollupCreator, _, validatorUtils, validatorWalletCreator, err := deployRollupCreator(ctx, parentChainReader, deployAuth, maxDataSize, hotshot, isUsingFeeToken)
	if err != nil {
		return nil, *new(common.Address), *new(common.Address), fmt.Errorf("error deploying rollup creator: %w", err)
	}

	var validatorAddrs []common.Address
	for i := uint64(1); i <= authorizeValidators; i++ {
		validatorAddrs = append(validatorAddrs, crypto.CreateAddress(validatorWalletCreator, i))
	}

	deployParams := rollupgen.RollupCreatorRollupDeploymentParams{
		Config:                    config,
		Validators:                validatorAddrs,
		MaxDataSize:               maxDataSize,
		NativeToken:               nativeToken,
		DeployFactoriesToL2:       false,
		MaxFeePerGasForRetryables: big.NewInt(0), // needed when utility factories are deployed
		BatchPosters:              batchPosters,
		BatchPosterManager:        batchPosterManager,
	}

	tx, err := rollupCreator.CreateRollup(
		deployAuth,
		deployParams,
	)

	ospAddr, err := rollupCreator.Osp(&bind.CallOpts{
		Pending: false,
		Context: ctx,
	})

	if err != nil {
		return nil, *new(common.Address), *new(common.Address), fmt.Errorf("error submitting create rollup tx: %w", err)
	}
	receipt, err := parentChainReader.WaitForTxApproval(ctx, tx)
	if err != nil {
		return nil, *new(common.Address), *new(common.Address), fmt.Errorf("error executing create rollup tx: %w", err)
	}
	info, err := rollupCreator.ParseRollupCreated(*receipt.Logs[len(receipt.Logs)-1])
	if err != nil {
		return nil, *new(common.Address), *new(common.Address), fmt.Errorf("error parsing rollup created log: %w", err)
	}

	return &chaininfo.RollupAddresses{
			Bridge:                 info.Bridge,
			Inbox:                  info.InboxAddress,
			SequencerInbox:         info.SequencerInbox,
			DeployedAt:             receipt.BlockNumber.Uint64(),
			Rollup:                 info.RollupAddress,
			NativeToken:            nativeToken,
			UpgradeExecutor:        info.UpgradeExecutor,
			ValidatorUtils:         validatorUtils,
			ValidatorWalletCreator: validatorWalletCreator,
		},
		ospAddr,
		info.ChallengeManager,
		nil
}

func createNonEspressoL1ValidatorPosterNode(ctx context.Context, t *testing.T, hotshotUrl string) (*NodeBuilder, func()) {
	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	builder.l1StackConfig.HTTPPort = 8545
	builder.l1StackConfig.WSPort = 8546
	builder.l1StackConfig.HTTPHost = "0.0.0.0"
	builder.l1StackConfig.HTTPVirtualHosts = []string{"*"}
	builder.l1StackConfig.WSHost = "0.0.0.0"
	builder.l1StackConfig.DataDir = t.TempDir()
	builder.l1StackConfig.WSModules = append(builder.l1StackConfig.WSModules, "eth")

	builder.chainConfig.ArbitrumChainParams.EnableEspresso = false

	builder.nodeConfig.Feed.Input.URL = []string{fmt.Sprintf("ws://127.0.0.1:%d", broadcastPort)}
	builder.nodeConfig.BatchPoster.Enable = true
	builder.nodeConfig.BatchPoster.ErrorDelay = 5 * time.Second
	builder.nodeConfig.BatchPoster.MaxSize = 41
	builder.nodeConfig.BatchPoster.PollInterval = 10 * time.Second
	builder.nodeConfig.BatchPoster.MaxDelay = -1000 * time.Hour
	builder.nodeConfig.BatchPoster.LightClientAddress = lightClientAddress
	builder.nodeConfig.BatchPoster.HotShotUrl = hotshotUrl
	builder.nodeConfig.BlockValidator.Enable = true
	builder.nodeConfig.BlockValidator.ValidationPoll = 2 * time.Second
	builder.nodeConfig.BlockValidator.ValidationServer.URL = fmt.Sprintf("ws://127.0.0.1:%d", arbValidationPort)
	builder.nodeConfig.BlockValidator.LightClientAddress = lightClientAddress
	builder.nodeConfig.BlockValidator.Espresso = false
	builder.nodeConfig.DelayedSequencer.Enable = false

	cleanup := builder.BuildNonEspresso(t)

	// Fund the commitment task
	mnemonic := "indoor dish desk flag debris potato excuse depart ticket judge file exit"
	err := builder.L1Info.GenerateAccountWithMnemonic("CommitmentTask", mnemonic, 5)
	Require(t, err)
	builder.L1.TransferBalance(t, "Faucet", "CommitmentTask", big.NewInt(9e18), builder.L1Info)

	// Fund the stakers
	builder.L1Info.GenerateAccount("Staker1")
	builder.L1.TransferBalance(t, "Faucet", "Staker1", big.NewInt(9e18), builder.L1Info)
	builder.L1Info.GenerateAccount("Staker2")
	builder.L1.TransferBalance(t, "Faucet", "Staker2", big.NewInt(9e18), builder.L1Info)
	builder.L1Info.GenerateAccount("Staker3")
	builder.L1.TransferBalance(t, "Faucet", "Staker3", big.NewInt(9e18), builder.L1Info)

	// Update the rollup
	deployAuth := builder.L1Info.GetDefaultTransactOpts("RollupOwner", ctx)
	upgradeExecutor, err := upgrade_executorgen.NewUpgradeExecutor(builder.L2.ConsensusNode.DeployInfo.UpgradeExecutor, builder.L1.Client)
	Require(t, err)
	rollupABI, err := abi.JSON(strings.NewReader(rollupgen.RollupAdminLogicMetaData.ABI))
	Require(t, err)

	setMinAssertPeriodCalldata, err := rollupABI.Pack("setMinimumAssertionPeriod", big.NewInt(0))
	Require(t, err, "unable to generate setMinimumAssertionPeriod calldata")
	tx, err := upgradeExecutor.ExecuteCall(&deployAuth, builder.L2.ConsensusNode.DeployInfo.Rollup, setMinAssertPeriodCalldata)
	Require(t, err, "unable to set minimum assertion period")
	_, err = builder.L1.EnsureTxSucceeded(tx)
	Require(t, err)

	// Add the stakers into the validator whitelist
	staker1Addr := builder.L1Info.GetAddress("Staker1")
	staker2Addr := builder.L1Info.GetAddress("Staker2")
	staker3Addr := builder.L1Info.GetAddress("Staker3")
	setValidatorCalldata, err := rollupABI.Pack("setValidator", []common.Address{staker1Addr, staker2Addr, staker3Addr}, []bool{true, true, true})
	Require(t, err, "unable to generate setValidator calldata")
	tx, err = upgradeExecutor.ExecuteCall(&deployAuth, builder.L2.ConsensusNode.DeployInfo.Rollup, setValidatorCalldata)
	Require(t, err, "unable to set validators")
	_, err = builder.L1.EnsureTxSucceeded(tx)
	Require(t, err)

	return builder, cleanup
}

func createNonEspressoL2Node(ctx context.Context, t *testing.T, hotshot_url string, builder *NodeBuilder) (*TestClient, info, func()) {
	nodeConfig := arbnode.ConfigDefaultL1Test()
	builder.takeOwnership = false
	nodeConfig.BatchPoster.Enable = false
	nodeConfig.BlockValidator.Enable = false
	nodeConfig.DelayedSequencer.Enable = true
	nodeConfig.Sequencer = true
	nodeConfig.Espresso = false
	builder.execConfig.Sequencer.LightClientAddress = lightClientAddress
	builder.execConfig.Sequencer.SwitchPollInterval = 10 * time.Second
	builder.execConfig.Sequencer.SwitchDelayThreshold = uint64(delayThreshold)
	builder.execConfig.Sequencer.Enable = true
	builder.execConfig.Sequencer.Espresso = false
	builder.execConfig.Sequencer.EspressoNamespace = builder.chainConfig.ChainID.Uint64()
	builder.execConfig.Sequencer.HotShotUrl = hotshot_url

	builder.chainConfig.ArbitrumChainParams.EnableEspresso = false

	nodeConfig.Feed.Output.Enable = true
	nodeConfig.Feed.Output.Addr = "0.0.0.0"
	nodeConfig.Feed.Output.Enable = true
	nodeConfig.Feed.Output.Port = fmt.Sprintf("%d", broadcastPort)

	client, cleanup := builder.Build2ndNode(t, &SecondNodeParams{nodeConfig: nodeConfig})
	return client, builder.L2Info, cleanup
}

func BuildNonEspressoNetwork(ctx context.Context, t *testing.T) (*NodeBuilder, *TestClient, *BlockchainTestInfo, func()) {
	cleanValNode := createValidationNode(ctx, t, false)

	builder, cleanup := createNonEspressoL1ValidatorPosterNode(ctx, t, hotShotUrl)

	err := waitFor(t, ctx, func() bool {
		if e := exec.Command(
			"curl",
			"-X",
			"POST",
			"-H",
			"Content-Type: application/json",
			"-d",
			"{'jsonrpc':'2.0','id':45678,'method':'eth_chainId','params':[]}",
			"http://localhost:8545",
		).Run(); e != nil {
			return false
		}
		return true
	})
	Require(t, err)

	/* wait for the builder
	err = waitForWith(t, ctx, 40*time.Second, 1*time.Second, func() bool {
		out, err := exec.Command("curl", "http://localhost:41000/availability/block/10", "-L").Output()
		if err != nil {
			log.Warn("retry to check the builder", "err", err)
			return false
		}
		return len(out) > 0
	})
	Require(t, err)
	*/
	l2Node, l2Info, cleanL2Node := createNonEspressoL2Node(ctx, t, hotShotUrl, builder)

	return builder, l2Node, l2Info, func() {
		cleanL2Node()
		cleanup()
		cleanValNode()
	}
}

func TestEspressoMigration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder, l2Node, l2Info, cleanup := BuildNonEspressoNetwork(ctx, t)
	defer cleanup()
	node := builder.L2

	log.Info("Pre initial message")
	// Wait for the initial message
	expected := arbutil.MessageIndex(1)
	err := waitFor(t, ctx, func() bool {
		msgCnt, err := l2Node.ConsensusNode.TxStreamer.GetMessageCount()
		if err != nil {
			panic(err)
		}

		validatedCnt := node.ConsensusNode.BlockValidator.Validated(t)
		return msgCnt >= expected && validatedCnt >= expected
	})
	Require(t, err)

	preEspressoAccount := "preEspressoAccount"
	err = checkTransferTxOnL2(t, ctx, l2Node, preEspressoAccount, l2Info)

	/*// wait for the latest hotshot block
	err = waitFor(t, ctx, func() bool {
		out, err := exec.Command("curl", "http://127.0.0.1:41000/status/block-height", "-L").Output()
		if err != nil {
			return false
		}
		h := 0
		err = json.Unmarshal(out, &h)
		if err != nil {
			return false
		}
		return h > 0
	})
	Require(t, err)
	*/

	// Check if the tx is executed correctly
	err = checkTransferTxOnL2(t, ctx, l2Node, "User10", l2Info)
	Require(t, err)

	// Remember the number of messages
	var msgCnt arbutil.MessageIndex
	err = waitFor(t, ctx, func() bool {
		cnt, err := node.ConsensusNode.TxStreamer.GetMessageCount()
		Require(t, err)
		msgCnt = cnt
		log.Info("waiting for message count", "cnt", msgCnt)
		return msgCnt > 2
	})
	Require(t, err)

	// Wait for the number of validated messages to catch up
	err = waitForWith(t, ctx, 360*time.Second, 5*time.Second, func() bool {
		validatedCnt := node.ConsensusNode.BlockValidator.Validated(t)
		log.Info("waiting for validation", "validatedCnt", validatedCnt, "msgCnt", msgCnt)
		return validatedCnt >= msgCnt
	})
	Require(t, err)

	ChallengeManagerIndex := "ChallengeManager"
	challengeManagerAddress := builder.L1Info.Accounts[ChallengeManagerIndex].Address
	Require(t, err)

	l1Client := builder.L1.Client

	challengeManager, err := challengegen.NewChallengeManager(challengeManagerAddress, l1Client)
	Require(t, err)

	upgradeExecutorAddress := builder.L1Info.GetAddress("UpgradeExecutor")
	upgradeExecutor, err := upgrade_executorgen.NewUpgradeExecutor(upgradeExecutorAddress, l1Client)

	Require(t, err)

	upgradeExecutor.ExecuteCall()

	//challengeManager.PostUpgradeInit()
	newAccount2 := "User11"
	l2Info.GenerateAccount(newAccount2)
	addr2 := l2Info.GetAddress(newAccount2)

	// Transfer via the delayed inbox
	delayedTx := l2Info.PrepareTx("Owner", newAccount2, 3e7, transferAmount, nil)
	builder.L1.SendWaitTestTransactions(t, []*types.Transaction{
		WrapL2ForDelayed(t, delayedTx, builder.L1Info, "Faucet", 100000),
	})
	err = waitForWith(t, ctx, 180*time.Second, 2*time.Second, func() bool {
		balance2 := l2Node.GetBalance(t, addr2)
		log.Info("waiting for balance", "account", newAccount2, "addr", addr2, "balance", balance2)
		return balance2.Cmp(transferAmount) >= 0
	})
	Require(t, err)

	incorrectHeight := uint64(10)

	goodStaker, blockValidatorA, cleanA := createStaker(ctx, t, builder, 0, "Staker1", nil)
	defer cleanA()
	badStaker1, blockValidatorB, cleanB := createStaker(ctx, t, builder, incorrectHeight, "Staker2", func(input *validator.ValidationInput) {
		log.Info("previousinput", "input", input.HotShotCommitment)
		input.HotShotCommitment = espressoTypes.Commitment{}
		log.Info("afterinput", "input", input.HotShotCommitment)
	})
	defer cleanB()
	badStaker2, blockValidatorC, cleanC := createStaker(ctx, t, builder, incorrectHeight, "Staker3", func(input *validator.ValidationInput) {
		input.HotShotLiveness = !input.HotShotLiveness
	})
	defer cleanC()

	err = waitForWith(t, ctx, 240*time.Second, 1*time.Second, func() bool {
		validatedA := blockValidatorA.Validated(t)
		validatedB := blockValidatorB.Validated(t)
		validatorC := blockValidatorC.Validated(t)
		shouldValidated := arbutil.MessageIndex(incorrectHeight - 1)
		condition := validatedA >= shouldValidated && validatedB >= shouldValidated && validatorC >= shouldValidated
		if !condition {
			log.Info("waiting for stakers to catch up the incorrect hotshot height", "stakerA", validatedA, "stakerB", validatedB, "target", shouldValidated)
		}
		return condition
	})
	Require(t, err)
	validatorUtils, err := rollupgen.NewValidatorUtils(builder.L2.ConsensusNode.DeployInfo.ValidatorUtils, builder.L1.Client)
	Require(t, err)
	goodOpts := builder.L1Info.GetDefaultCallOpts("Staker1", ctx)
	badOpts1 := builder.L1Info.GetDefaultCallOpts("Staker2", ctx)
	badOpts2 := builder.L1Info.GetDefaultCallOpts("Staker3", ctx)
	i := 0
	err = waitForWith(t, ctx, 60*time.Second, 2*time.Second, func() bool {
		log.Info("good staker acts", "step", i)
		txA, err := goodStaker.Act(ctx)
		Require(t, err)
		if txA != nil {
			_, err = builder.L1.EnsureTxSucceeded(txA)
			Require(t, err)
		}

		log.Info("bad staker1 acts", "step", i)
		txB, err := badStaker1.Act(ctx)
		Require(t, err)
		if txB != nil {
			_, err = builder.L1.EnsureTxSucceeded(txB)
			Require(t, err)
		}

		log.Info("bad staker2 acts", "step", i)
		txC, err := badStaker2.Act(ctx)
		Require(t, err)
		if txC != nil {
			_, err = builder.L1.EnsureTxSucceeded(txC)
			Require(t, err)
		}

		i += 1
		conflict1, err := validatorUtils.FindStakerConflict(nil, builder.L2.ConsensusNode.DeployInfo.Rollup, goodOpts.From, badOpts1.From, big.NewInt(1024))
		Require(t, err)
		conflict2, err := validatorUtils.FindStakerConflict(nil, builder.L2.ConsensusNode.DeployInfo.Rollup, goodOpts.From, badOpts2.From, big.NewInt(1024))
		Require(t, err)
		condition := staker.ConflictType(conflict1.Ty) == staker.CONFLICT_TYPE_FOUND && staker.ConflictType(conflict2.Ty) == staker.CONFLICT_TYPE_FOUND
		if !condition {
			log.Info("waiting for the conflict")
		}
		return condition
	})
	Require(t, err)

	// The following tests are very time-consuming and, given that the related code
	// does not change often, it's not necessary to run them every time.
	// Note: If you are modifying the smart contracts, staker-related code or doing overhaul,
	// set the E2E_CHECK_STAKER env variable to any non-empty string to run the check.

	checkStaker := os.Getenv("E2E_CHECK_STAKER")
	if checkStaker == "" {
		log.Info("Checking the escape hatch")
		// Start to check the escape hatch
		address := common.HexToAddress(lightClientAddress)

		txOpts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
		// Freeze the l1 height
		err := lightclientmock.FreezeL1Height(t, builder.L1.Client, address, &txOpts)
		Require(t, err)

		err = waitForWith(t, ctx, 1*time.Minute, 1*time.Second, func() bool {
			isLive, err := lightclientmock.IsHotShotLive(t, builder.L1.Client, address, uint64(delayThreshold))
			if err != nil {
				return false
			}
			return !isLive
		})
		Require(t, err)

		// Wait for the switch to be totally finished
		currMsg, err := node.ConsensusNode.TxStreamer.GetMessageCount()
		Require(t, err)
		var validatedMsg arbutil.MessageIndex
		err = waitForWith(t, ctx, 6*time.Minute, 60*time.Second, func() bool {

			validatedCnt := node.ConsensusNode.BlockValidator.Validated(t)
			if validatedCnt >= currMsg {
				validatedMsg = validatedCnt
				return true
			}
			return false
		})
		Require(t, err)

		err = checkTransferTxOnL2(t, ctx, l2Node, "User12", l2Info)
		Require(t, err)
		err = checkTransferTxOnL2(t, ctx, l2Node, "User13", l2Info)
		Require(t, err)

		err = waitForWith(t, ctx, 3*time.Minute, 20*time.Second, func() bool {
			validated := node.ConsensusNode.BlockValidator.Validated(t)
			return validated >= validatedMsg
		})
		Require(t, err)

		// Unfreeze the l1 height
		err = lightclientmock.UnfreezeL1Height(t, builder.L1.Client, address, &txOpts)
		Require(t, err)

		// Check if the validated count is increasing
		err = waitForWith(t, ctx, 3*time.Minute, 20*time.Second, func() bool {
			validated := node.ConsensusNode.BlockValidator.Validated(t)
			return validated >= validatedMsg+10
		})
		Require(t, err)

		return
	}
	err = waitForWith(
		t,
		ctx,
		time.Minute*20,
		time.Second*5,
		func() bool {
			log.Info("good staker acts", "step", i)
			txA, err := goodStaker.Act(ctx)
			if err != nil {
				return false
			}
			if txA != nil {
				_, err = builder.L1.EnsureTxSucceeded(txA)
				Require(t, err)
			}

			log.Info("bad staker acts", "step", i)
			txB, err := badStaker1.Act(ctx)
			if txB != nil && err == nil {
				_, err = builder.L1.EnsureTxSucceeded(txB)
				Require(t, err)
			} else if err != nil {
				ok := strings.Contains(err.Error(), "ERROR_HOTSHOT_COMMITMENT")
				if ok {
					return true
				} else {
					fmt.Println(err.Error())
					t.Fatal("unexpected err")
				}
			}
			i += 1
			return false

		})
	Require(t, err)
}
