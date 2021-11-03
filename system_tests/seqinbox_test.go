//
// Copyright 2021, Offchain Labs, Inc. All rights reserved.
//

package arbtest

import (
	"bytes"
	"context"
	"math/big"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/offchainlabs/arbstate/arbnode"
	"github.com/offchainlabs/arbstate/arbos"
	"github.com/offchainlabs/arbstate/solgen/go/bridgegen"
)

type blockTestState struct {
	balances      map[common.Address]uint64
	accounts      []common.Address
	l2BlockNumber uint64
	l1BlockNumber uint64
}

func TestSequencerInboxReader(t *testing.T) {
	l2Backend, l2Info := CreateTestL2(t)
	l1Client, l1BlockChain, l1Info := CreateTestL1(t, l2Backend)

	seqInbox, err := bridgegen.NewSequencerInbox(l1Info.GetAddress("SequencerInbox"), l1Client)
	if err != nil {
		t.Fatal(err)
	}
	seqOpts := l1Info.GetDefaultTransactOpts("Sequencer")

	ownerAddress := l2Info.GetAddress("Owner")
	startL2BlockNumber := l2Backend.APIBackend().CurrentHeader().Number.Uint64()

	startState, _, err := l2Backend.APIBackend().StateAndHeaderByNumber(context.Background(), rpc.LatestBlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	startOwnerBalance := startState.GetBalance(ownerAddress).Uint64()

	var blockStates []blockTestState
	blockStates = append(blockStates, blockTestState{
		balances: map[common.Address]uint64{
			ownerAddress: startOwnerBalance,
		},
		accounts:      []common.Address{ownerAddress},
		l2BlockNumber: startL2BlockNumber,
	})

	accountName := func(x int) string {
		if x == 0 {
			return "Owner"
		} else {
			return "Account" + strconv.Itoa(x)
		}
	}

	for i := 1; i < 100; i++ {
		if i%10 == 0 {
			reorgTo := rand.Int() % len(blockStates)
			reorgTarget := l1BlockChain.GetBlockByNumber(blockStates[reorgTo].l1BlockNumber)
			err := l1BlockChain.ReorgToOldBlock(reorgTarget)
			if err != nil {
				t.Fatal(err)
			}
			blockStates = blockStates[:(reorgTo + 1)]
		} else {
			state := blockStates[len(blockStates)-1]
			newBalances := make(map[common.Address]uint64)
			for k, v := range state.balances {
				newBalances[k] = v
			}
			state.balances = newBalances

			var batchSegments [][]byte
			numMessages := rand.Int() % 5
			for j := 0; j < numMessages; j++ {
				sourceNum := rand.Int() % len(state.accounts)
				source := state.accounts[sourceNum]
				amount := uint64(rand.Int()) % state.balances[source]
				if state.balances[source]-amount < 100000000 || amount == 0 {
					// Leave enough funds for gas
					amount = 1
				}
				var dest common.Address
				var destNum int
				if j == 0 {
					destNum = len(state.accounts)
					name := accountName(destNum)
					l2Info.GenerateAccount(name)
					dest = l2Info.GetAddress(name)
					state.accounts = append(state.accounts, dest)
				} else {
					destNum = rand.Int() % len(state.accounts)
					dest = state.accounts[destNum]
				}

				tx := l2Info.PrepareTx(accountName(sourceNum), accountName(destNum), 50001, big.NewInt(1e6), nil)
				txData, err := tx.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				var segment []byte
				segment = append(segment, 0) // segmentKindL2Message
				segment = append(segment, arbos.L2MessageKind_SignedTx)
				segment = append(segment, txData...)
				batchSegments = append(batchSegments, segment)

				state.balances[source] -= amount
				state.balances[dest] += amount
			}

			batchBuffer := new(bytes.Buffer)
			writer := brotli.NewWriter(batchBuffer)
			err := rlp.Encode(writer, batchSegments)
			if err != nil {
				t.Fatal(err)
			}
			err = writer.Close()
			if err != nil {
				t.Fatal(err)
			}
			batchData := batchBuffer.Bytes()

			tx, err := seqInbox.AddSequencerL2BatchFromOrigin(&seqOpts, big.NewInt(int64(len(blockStates)-1)), batchData, big.NewInt(0), common.Address{})
			if err != nil {
				t.Fatal(err)
			}
			txRes, err := arbnode.EnsureTxSucceeded(l1Client, tx)
			if err != nil {
				t.Fatal(err)
			}

			state.l2BlockNumber += uint64(numMessages)
			state.l1BlockNumber = txRes.BlockNumber.Uint64()
			blockStates = append(blockStates, state)
		}

		expectedBlockNumber := blockStates[len(blockStates)-1].l2BlockNumber
		for i := 0; ; i++ {
			blockNumber := l2Backend.APIBackend().CurrentHeader().Number.Uint64()
			if blockNumber == expectedBlockNumber {
				break
			} else if i >= 100 {
				t.Fatal("timed out waiting for l2 block update; have", blockNumber, "want", expectedBlockNumber)
			}
			time.Sleep(10 * time.Millisecond)
		}

		for _, state := range blockStates {
			block, err := l2Backend.APIBackend().BlockByNumber(context.Background(), rpc.BlockNumber(state.l2BlockNumber))
			if err != nil {
				t.Fatal(err)
			}
			if block == nil {
				t.Fatal("missing state block", state.l2BlockNumber)
			}
			stateDb, _, err := l2Backend.APIBackend().StateAndHeaderByNumber(context.Background(), rpc.BlockNumber(state.l2BlockNumber))
			if err != nil {
				t.Fatal(err)
			}
			for acct, balance := range state.balances {
				haveBalance := stateDb.GetBalance(acct)
				if new(big.Int).SetUint64(balance).Cmp(haveBalance) < 0 {
					t.Error("unexpected balance for account", acct, "; expected", balance, "got", haveBalance)
				}
			}
		}
	}
}
