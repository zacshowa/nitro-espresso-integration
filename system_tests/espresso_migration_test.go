package arbtest

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/offchainlabs/nitro/solgen/go/challengegen"
	"github.com/offchainlabs/nitro/solgen/go/mocksgen"
	"github.com/offchainlabs/nitro/solgen/go/precompilesgen"
	"github.com/offchainlabs/nitro/util/arbmath"
	"github.com/offchainlabs/nitro/util/headerreader"
	"github.com/offchainlabs/nitro/validator/server_common"

	espressoOspGen "github.com/offchainlabs/nitro/solgen/go/ospgen"
	"github.com/offchainlabs/nitro/solgen/go/upgrade_executorgen"
	"math/big"
	"os/exec"
	"strings"
	"testing"
	"time"

	espressoTypes "github.com/EspressoSystems/espresso-sequencer-go/types"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/solgen/go/rollupgen"
	"github.com/offchainlabs/nitro/staker"
	"github.com/offchainlabs/nitro/validator"
)

func BuildNonEspressoNetwork(ctx context.Context, t *testing.T) (*NodeBuilder, *TestClient, *BlockchainTestInfo, *SecondNodeParams, func(), func()) {
	cleanValNode := createValidationNode(ctx, t, false)

	builder, cleanup := createL1ValidatorPosterNode(ctx, t, hotShotUrl, false)

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

	l2Node, l2Info, secondNodeParams, cleanL2Node := createL2Node(ctx, t, hotShotUrl, builder, false)

	return builder, l2Node, l2Info, secondNodeParams, cleanL2Node, func() {
		cleanup()
		cleanValNode()
	}
}

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

func DeployNewOspToL1(t *testing.T, ctx context.Context, l1client client, hotshot common.Address, auth *bind.TransactOpts) (common.Address, common.Address, error) {
	arbSys, _ := precompilesgen.NewArbSys(types.ArbSysAddress, l1client)
	l1Reader, err := headerreader.New(ctx, l1client, func() *headerreader.Config { return &headerreader.TestConfig }, arbSys)
	Require(t, err)
	l1Reader.Start(ctx)
	defer l1Reader.StopAndWait()

	client := l1Reader.Client()

	osp0, tx, _, err := espressoOspGen.DeployOneStepProver0(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("osp0 deploy error: %w", err)
	}

	ospMem, tx, _, err := espressoOspGen.DeployOneStepProverMemory(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospMemory deploy error: %w", err)
	}

	ospMath, tx, _, err := espressoOspGen.DeployOneStepProverMath(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospMath deploy error: %w", err)
	}

	ospHostIo, tx, _, err := espressoOspGen.DeployOneStepProverHostIo(auth, client, hotshot)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospHostIo deploy error: %w", err)
	}

	challengeManagerAddr, tx, _, err := challengegen.DeployChallengeManager(auth, client)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("challenge manager deploy error: %w", err)
	}

	ospEntryAddr, tx, _, err := espressoOspGen.DeployOneStepProofEntry(auth, client, osp0, ospMem, ospMath, ospHostIo)
	err = andTxSucceeded(ctx, l1Reader, tx, err)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("ospEntry deploy error: %w", err)
	}

	return ospEntryAddr, challengeManagerAddr, nil
}

// Name: errorIfAddressesEqual
// Desc: test utility function that will throw an error if 2 addresses are equal, useful for checking contract upgrades have succeeded.
func errorIfAddressesEqual(addr1 common.Address, addr2 common.Address) error {
	if addr1 == addr2 {
		return fmt.Errorf("address %s and address %s are the same", addr1, addr2)
	}
	return nil
}

// Name: errorIfAddressesUnequal
// Desc: test utility function that will throw an error if 2 addresses are unequal, useful for checking contract upgrades have succeeded.
func errorIfAddressesUnequal(addr1 common.Address, addr2 common.Address) error {
	if addr1 != addr2 {
		return fmt.Errorf("address %s and address %s are not the same", addr1, addr2)
	}
	return nil
}

func printAccounts(builder *NodeBuilder) {
	account_names := [...]string{"Bridge", "ChallengeManager", "CommitmentTask", "Faucet", "Inbox", "OspEntry", "RollupOwner", "Sequencer", "SequencerInbox", "UpgradeExecutor", "User", "Validator", "Staker1", "Staker2", "Staker3"}

	for _, account_name := range account_names {
		account_info := builder.L1Info.GetAddress(account_name)
		//acounts := [...]*AccountInfo{account_info}
		log.Info("Parsing accounts", "account_name", account_name, "account_info", account_info)
	}
}

func TestEspressoMigration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder, l2Node, l2Info, secondNodeParams, _, cleanup := BuildNonEspressoNetwork(ctx, t)
	defer cleanup()
	node := builder.L2
	l1Client := builder.L1.Client

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
	Require(t, err)
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

	log.Info("Turn on espresso network after sequencing some default transactions.")

	shutdown := runEspresso(t, ctx)
	defer shutdown()
	log.Info("prepare to turn on espresso sequencing by changing the builders configs.")
	//Change various config flags to enable espresso behavior in the L2 node, these are all references so in theory we should be able to edit these mid-test to enable the espresso behavior.
	builder.chainConfig.ArbitrumChainParams.EnableEspresso = true
	secondNodeParams.nodeConfig.Espresso = true
	builder.execConfig.Sequencer.Espresso = true

	//get the current wasmmoduleroot.
	locator, err := server_common.NewMachineLocator(builder.valnodeConfig.Wasm.RootPath)
	Require(t, err)

	wasmModuleRoot := locator.LatestWasmModuleRoot()

	//deploy the new OSP contracts to the L1 and record their addresses/
	auth := builder.L1Info.GetDefaultTransactOpts("RollupOwner", ctx)
	newOspEntry, newChallengeManagerAddr, err := DeployNewOspToL1(t, ctx, builder.L1.Client, common.HexToAddress(builder.nodeConfig.BlockValidator.LightClientAddress), &auth)
	//construct light client addr.
	Require(t, err)

	log.Info("Get challenge manager account")
	ChallengeManagerIndex := "ChallengeManager"
	challengeManagerAddress := builder.L1Info.GetAddress(ChallengeManagerIndex)

	challengeManager, err := challengegen.NewChallengeManager(challengeManagerAddress, l1Client)
	Require(t, err)

	UpgradeExecutorIndex := "UpgradeExecutor"
	upgradeExecutorAddress := builder.L1Info.GetAddress(UpgradeExecutorIndex)

	OldOspEntryIndex := "OspEntry"
	OldOspEntryAddress := builder.L1Info.GetAddress(OldOspEntryIndex)

	log.Info("UpgradeOpts", "Opts:", auth)
	callDataAbi, err := abi.JSON(strings.NewReader(challengegen.ChallengeManagerMetaData.ABI))
	Require(t, err)
	log.Info("Call Data:", "newOspEntry", newOspEntry, "wasmModuleRoot", wasmModuleRoot, "OldOspEntry", OldOspEntryAddress)
	callData, err := callDataAbi.Pack("postUpgradeInit", newOspEntry, wasmModuleRoot, OldOspEntryAddress)
	Require(t, err)

	log.Info("callData", "data", callData)
	//	proxyAdminAddrStr := "0x9DE2c8DEd268d1ae6Db7d0a2fC2460e104616062"

	//	proxyAdminAddr := common.HexToAddress(proxyAdminAddrStr)

	proxyAdminSlot := common.BigToHash(arbmath.BigSub(crypto.Keccak256Hash([]byte("eip1967.proxy.admin")).Big(), common.Big1))
	proxyAdminBytes, err := builder.L1.Client.StorageAt(ctx, challengeManagerAddress, proxyAdminSlot, nil)
	Require(t, err)
	proxyAdminAddr := common.BytesToAddress(proxyAdminBytes)
	if proxyAdminAddr == (common.Address{}) {
		Fatal(t, "failed to get challenge manager proxy admin")
	}
	log.Info("ProxyAdmin", "addr", proxyAdminAddr)
	proxyAdminAbi, err := abi.JSON(strings.NewReader(mocksgen.ProxyAdminForBindingMetaData.ABI))
	Require(t, err)

	//proxyAdmin, err := mocksgen.NewProxyAdminForBinding(proxyAdminAddr, l1Client)
	Require(t, err)

	proxyAdminCallData, err := proxyAdminAbi.Pack("upgradeAndCall", challengeManagerAddress, newChallengeManagerAddr, callData) // change 2nd chall addr to actual impl addr
	Require(t, err)

	upgradeExecutor, err := upgrade_executorgen.NewUpgradeExecutor(upgradeExecutorAddress, l1Client)

	Require(t, err)

	tx, err := upgradeExecutor.ExecuteCall(&auth, proxyAdminAddr, proxyAdminCallData)
	Require(t, err)

	receipt, err := builder.L1.EnsureTxSucceeded(tx)
	Require(t, err)
	log.Info("receipt", "receipt", receipt)

	callOpts := builder.L1Info.GetDefaultCallOpts("RollupOwner", ctx)
	updatedOspAddr, err := challengeManager.Osp(callOpts)
	Require(t, err)

	err = errorIfAddressesEqual(updatedOspAddr, OldOspEntryAddress)
	Require(t, err)
	log.Info("updatedOspAddr", "updatedOspAddr", updatedOspAddr, "OldOspEntryAddress", OldOspEntryAddress, "newOspEntry", newOspEntry)
	err = errorIfAddressesUnequal(updatedOspAddr, newOspEntry)
	Require(t, err) // TODO: Left off here, Next is to use the proxy admin to get the challenge managers implementation address and attempt a "no-op" upgrade

	log.Info("Successfully upgraded the challenge manager to point to the new OSP entry.")

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
}
