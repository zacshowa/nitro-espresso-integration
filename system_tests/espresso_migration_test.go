package arbtest

import (
	"context"
	"github.com/ethereum/go-ethereum/accounts/abi"

	espressoOspGen "github.com/offchainlabs/nitro/solgen/go/ospgen"
	"github.com/offchainlabs/nitro/solgen/go/upgrade_executorgen"
	"github.com/stretchr/testify/require"
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

func deployEspressoOSP() error {
	return nil
}

func BuildNonEspressoNetwork(ctx context.Context, t *testing.T) (*NodeBuilder, *TestClient, *BlockchainTestInfo, func(), func()) {
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

	l2Node, l2Info, cleanL2Node := createL2Node(ctx, t, hotShotUrl, builder, false)

	return builder, l2Node, l2Info, cleanL2Node, func() {
		cleanup()
		cleanValNode()
	}
}

func TestEspressoMigration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder, l2Node, l2Info, l2cleanup, cleanup := BuildNonEspressoNetwork(ctx, t)
	defer cleanup()
	node := builder.L2

	builder.chainConfig.ArbitrumChainParams.EnableEspresso = true
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
	// We have seen the non-espresso network sequence transactions, we need to shutdown the L2 node, and then start a new one with the new chain config or update it's chain config to use the espresso logic,
	// TODO: Determine If we are shutting down the node and restarting a new one with a new chain config or paying to update the chain config on the running node such that the espresso code can be loaded onto the sequencer box ahead of time.
	ChallengeManagerIndex := "ChallengeManager"
	challengeManagerAddress := builder.L1Info.Accounts[ChallengeManagerIndex].Address
	Require(t, err)

	l1Client := builder.L1.Client

	Require(t, err)

	upgradeExecutorAddress := builder.L1Info.GetAddress("UpgradeExecutor")
	upgradeExecutor, err := upgrade_executorgen.NewUpgradeExecutor(upgradeExecutorAddress, l1Client)

	Require(t, err)

	upgradeTransactionOpts := builder.L1Info.GetDefaultTransactOpts("RollupOwner", ctx)

	callDataAbi, err := abi.JSON(strings.NewReader(espressoOspGen.OneStepProverHostIoMetaData.ABI))

	callData, err := callDataAbi.Pack("PostUpgradeInit") //TODO: I left off here and I need to figure out wasmmodule root stuff to construct the conract call
	Require(t, err)

	require.NoError(t, err)

	res, err := upgradeExecutor.ExecuteCall(&upgradeTransactionOpts, challengeManagerAddress, callData)
	Require(t, err)

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
}
