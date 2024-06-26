package arbtest

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	testingClient "github.com/EspressoSystems/espresso-sequencer-go/client"
	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/arbutil"
)

func newWaitFor(
	t *testing.T,
	ctxinput context.Context,
	condition func() (int, bool),
) (int, error) {
	return newWaitForWith(t, ctxinput, 30*time.Second, time.Second, condition)
}

func newWaitForWith(
	t *testing.T,
	ctxinput context.Context,
	timeout time.Duration,
	interval time.Duration,
	condition func() (int, bool),
) (int, error) {
	ctx, cancel := context.WithTimeout(ctxinput, timeout)
	defer cancel()

	for {
		val, cond := condition()
		if cond {
			return val, nil
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
}

// create a trial smoke test to get an understanding of the testing framework.
func TestTrialSmokeTest(t *testing.T) {

	client := testingClient.NewClient(hotShotUrl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder, l2Node, _, cleanup := runNodes(ctx, t)
	defer cleanup()
	node := builder.L2

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

	// wait for the latest hotshot block
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

	//fetch the current height of the network with the test client so we can actually test with the espresso client.
	height, err := l2Node.Client.BlockNumber(ctx)

	if err != nil {
		log.Error(err.Error())
		panic(err)
	}

	// Attempt to fetch the transactions in the block at the height returned by the test client.
	_, err = client.FetchTransactionsInBlock(ctx, height, 0)

	if err != nil {
		log.Error(err.Error())
		panic(err)
	}
	Require(t, err)

}
