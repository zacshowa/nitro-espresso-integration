//
// Copyright 2021, Offchain Labs, Inc. All rights reserved.
//

package arbstate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/andybalholm/brotli"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/offchainlabs/arbstate/arbos"
)

type InboxBackend interface {
	PeekSequencerInbox() ([]byte, error)

	GetSequencerInboxPosition() uint64
	AdvanceSequencerInbox()

	GetPositionWithinMessage() uint64
	SetPositionWithinMessage(pos uint64)

	ReadDelayedInbox(seqNum uint64) ([]byte, error)
}

type MessageWithMetadata struct {
	Message             *arbos.L1IncomingMessage
	MustEndBlock        bool
	DelayedMessagesRead uint64
}

type InboxMultiplexer interface {
	Peek() (*MessageWithMetadata, error)
	Advance() error
	DelayedMessagesRead() uint64
}

type sequencerMessage struct {
	minTimestamp         uint64
	maxTimestamp         uint64
	minL1Block           uint64
	maxL1Block           uint64
	afterDelayedMessages uint64
	segments             [][]byte
}

const maxDecompressedLen int64 = 1024 * 1024 * 16 // 16 MiB

func parseSequencerMessage(data []byte) *sequencerMessage {
	if len(data) < 40 {
		panic("sequencer message missing L1 header")
	}
	minTimestamp := binary.BigEndian.Uint64(data[:8])
	maxTimestamp := binary.BigEndian.Uint64(data[8:16])
	minL1Block := binary.BigEndian.Uint64(data[16:24])
	maxL1Block := binary.BigEndian.Uint64(data[24:32])
	afterDelayedMessages := binary.BigEndian.Uint64(data[32:40])
	reader := io.LimitReader(brotli.NewReader(bytes.NewReader(data[40:])), maxDecompressedLen)
	var segments [][]byte
	err := rlp.NewStream(reader, uint64(maxDecompressedLen)).Decode(&segments)
	if err != nil {
		log.Warn("error parsing sequencer message segments", "err", err.Error())
		segments = nil
	}
	return &sequencerMessage{
		minTimestamp:         minTimestamp,
		maxTimestamp:         maxTimestamp,
		minL1Block:           minL1Block,
		maxL1Block:           maxL1Block,
		afterDelayedMessages: afterDelayedMessages,
		segments:             segments,
	}
}

func (m sequencerMessage) Encode() []byte {
	var header [40]byte
	binary.BigEndian.PutUint64(header[:8], m.minTimestamp)
	binary.BigEndian.PutUint64(header[8:16], m.maxTimestamp)
	binary.BigEndian.PutUint64(header[16:24], m.minL1Block)
	binary.BigEndian.PutUint64(header[24:32], m.maxL1Block)
	binary.BigEndian.PutUint64(header[32:40], m.afterDelayedMessages)
	buf := new(bytes.Buffer)
	segmentsEnc, err := rlp.EncodeToBytes(&m.segments)
	if err != nil {
		panic("couldn't encode sequencerMessage")
	}

	writer := brotli.NewWriter(buf)
	defer writer.Close()
	_, err = writer.Write(segmentsEnc)
	if err != nil {
		panic("Could not write")
	}
	writer.Flush()
	return append(header[:], buf.Bytes()...)
}

type inboxMultiplexer struct {
	backend                       InboxBackend
	delayedMessagesRead           uint64
	sequencerMessageCache         *sequencerMessage
	sequencerMessageCachePosition uint64

	advanceComputed  bool
	advanceSegmentTo uint64
	advanceDelayedTo uint64
	advanceMessage   bool
}

func NewInboxMultiplexer(backend InboxBackend, delayedMessagesRead uint64) InboxMultiplexer {
	return &inboxMultiplexer{
		backend:             backend,
		delayedMessagesRead: delayedMessagesRead,
	}
}

var SequencerAddress = common.HexToAddress("0xA4B000000000000000000073657175656e636572") // TODO

var invalidMessage *arbos.L1IncomingMessage = &arbos.L1IncomingMessage{
	Header: &arbos.L1IncomingMessageHeader{
		Kind: arbos.L1MessageType_Invalid,
	},
	L2msg: []byte{},
}

const segmentKindL2Message uint8 = 0
const segmentKindDelayedMessages uint8 = 1
const segmentKindAdvanceTimestamp uint8 = 2
const segmentKindAdvanceL1BlockNumber uint8 = 3

// Returns the next message without advancing, and any *backend* error
// This does *not* return parse errors, those are transformed into invalid messages
func (r *inboxMultiplexer) Peek() (*MessageWithMetadata, error) {
	seqMsgPosition := r.backend.GetSequencerInboxPosition()
	var seqMsg *sequencerMessage
	if r.sequencerMessageCache != nil && r.sequencerMessageCachePosition == seqMsgPosition {
		seqMsg = r.sequencerMessageCache
	} else {
		bytes, realErr := r.backend.PeekSequencerInbox()
		if realErr != nil {
			return nil, realErr
		}
		seqMsg = parseSequencerMessage(bytes)
		r.sequencerMessageCache = seqMsg
		r.sequencerMessageCachePosition = seqMsgPosition
	}

	msg, delayedTarget, parseErr := r.peekInternal(seqMsg)
	if parseErr != nil {
		log.Warn("error parsing sequencer message", "err", parseErr)
		delayedTarget = nil
		msg = &MessageWithMetadata{
			Message:             invalidMessage,
			MustEndBlock:        true,
			DelayedMessagesRead: r.delayedMessagesRead,
		}
	}

	var endSegment bool
	if delayedTarget != nil {
		if *delayedTarget <= r.delayedMessagesRead {
			// should never happen
			return nil, errors.New("attempted to read already read delayed messages")
		}

		data, realErr := r.backend.ReadDelayedInbox(r.delayedMessagesRead)
		if realErr != nil {
			return nil, realErr
		}
		delayed, parseErr := arbos.ParseIncomingL1Message(bytes.NewReader(data))
		if parseErr != nil {
			log.Warn("error parsing delayed message", "err", parseErr)
			delayed = invalidMessage
		}
		endOfMessage := r.delayedMessagesRead+1 >= *delayedTarget
		msg = &MessageWithMetadata{
			Message:             delayed,
			MustEndBlock:        endOfMessage,
			DelayedMessagesRead: r.delayedMessagesRead + 1,
		}

		r.advanceDelayedTo = r.delayedMessagesRead + 1
		endSegment = r.advanceDelayedTo == *delayedTarget
	} else {
		r.advanceDelayedTo = r.delayedMessagesRead
		endSegment = true
	}

	r.advanceMessage = false
	currentSegment := r.backend.GetPositionWithinMessage()
	if endSegment {
		// make sure we advance the segment
		if r.advanceSegmentTo <= currentSegment {
			r.advanceSegmentTo = currentSegment + 1
		}
		// check if we're advancing past the end of the message
		if r.advanceSegmentTo >= uint64(len(seqMsg.segments)) {
			if r.advanceDelayedTo >= seqMsg.afterDelayedMessages {
				// we're ready to move on to the next message
				r.advanceMessage = true
				r.advanceSegmentTo = 0
			} else {
				// we need to read more delayed messages
				// set the segment to just after the end
				r.advanceSegmentTo = uint64(len(seqMsg.segments))
			}
		}
	} else {
		// make sure we don't advance the segment
		r.advanceSegmentTo = currentSegment
	}
	r.advanceComputed = true

	return msg, nil
}

// Returns a message, the delayed messages being read up to if applicable, and any *parsing* error
func (r *inboxMultiplexer) peekInternal(seqMsg *sequencerMessage) (*MessageWithMetadata, *uint64, error) {
	segmentNum := r.backend.GetPositionWithinMessage()
	var timestamp uint64
	var blockNumber uint64
	for {
		if segmentNum >= uint64(len(seqMsg.segments)) {
			break
		}
		segment := seqMsg.segments[int(segmentNum)]
		if len(segment) == 0 {
			segmentNum++
			continue
		}
		segmentKind := segment[0]
		if segmentKind == segmentKindAdvanceTimestamp || segmentKind == segmentKindAdvanceL1BlockNumber {
			rd := bytes.NewReader(segment[1:])
			advancing, err := rlp.NewStream(rd, 16).Uint()
			if err != nil {
				log.Warn("error parsing sequencer advancing segment", "err", err)
				continue
			}
			if segmentKind == segmentKindAdvanceTimestamp {
				timestamp += advancing
			} else if segmentKind == segmentKindAdvanceL1BlockNumber {
				blockNumber += advancing
			}
			segmentNum++
		} else {
			break
		}
	}
	if timestamp < seqMsg.minTimestamp {
		timestamp = seqMsg.minTimestamp
	} else if timestamp > seqMsg.maxTimestamp {
		timestamp = seqMsg.maxTimestamp
	}
	if blockNumber < seqMsg.minL1Block {
		blockNumber = seqMsg.minL1Block
	} else if blockNumber > seqMsg.maxL1Block {
		blockNumber = seqMsg.maxL1Block
	}
	if segmentNum >= uint64(len(seqMsg.segments)) {
		if r.delayedMessagesRead < seqMsg.afterDelayedMessages {
			return nil, &seqMsg.afterDelayedMessages, nil
		}
		return nil, nil, fmt.Errorf("after end of sequencer message (size %v)", len(seqMsg.segments))
	}
	r.advanceSegmentTo = segmentNum + 1
	segment := seqMsg.segments[int(segmentNum)]
	if len(segment) == 0 {
		return nil, nil, errors.New("empty sequencer message segment")
	}
	segmentKind := segment[0]
	if segmentKind == segmentKindL2Message {
		// L2 message
		var blockNumberHash common.Hash
		copy(blockNumberHash[:], math.U256Bytes(new(big.Int).SetUint64(blockNumber)))
		var timestampHash common.Hash
		copy(blockNumberHash[:], math.U256Bytes(new(big.Int).SetUint64(timestamp)))
		var requestId common.Hash
		// TODO: a consistent request id. Right now we just don't set the request id when it isn't needed.
		if len(segment) < 2 || segment[1] != arbos.L2MessageKind_SignedTx {
			requestId[0] = 1 << 6
			binary.BigEndian.PutUint64(requestId[(32-16):(32-8)], r.backend.GetSequencerInboxPosition())
			binary.BigEndian.PutUint64(requestId[(32-8):], segmentNum)
		}
		msg := &MessageWithMetadata{
			Message: &arbos.L1IncomingMessage{
				Header: &arbos.L1IncomingMessageHeader{
					Kind:        arbos.L1MessageType_L2Message,
					Sender:      SequencerAddress,
					BlockNumber: blockNumberHash,
					Timestamp:   timestampHash,
					RequestId:   requestId,
					GasPriceL1:  common.Hash{},
				},
				L2msg: segment[1:],
			},
			MustEndBlock:        true,
			DelayedMessagesRead: r.delayedMessagesRead,
		}
		return msg, nil, nil
	} else if segmentKind == segmentKindDelayedMessages {
		// Delayed message reading
		rd := bytes.NewReader(segment[1:])
		reading, err := rlp.NewStream(rd, 16).Uint()
		if err != nil {
			return nil, nil, err
		}
		newRead := r.delayedMessagesRead + reading
		if newRead <= r.delayedMessagesRead || newRead > seqMsg.afterDelayedMessages {
			return nil, nil, errors.New("bad delayed message reading count")
		}
		return nil, &newRead, nil
	} else {
		return nil, nil, fmt.Errorf("bad sequencer message segment kind %v", segmentKind)
	}
}

func (r *inboxMultiplexer) Advance() error {
	if !r.advanceComputed {
		_, realErr := r.Peek()
		if realErr != nil {
			return realErr
		}
		if !r.advanceComputed {
			panic("Failed to compute advance action")
		}
	}
	r.delayedMessagesRead = r.advanceDelayedTo
	r.backend.SetPositionWithinMessage(r.advanceSegmentTo)
	if r.advanceMessage {
		r.backend.AdvanceSequencerInbox()
	}
	r.advanceComputed = false
	return nil
}

func (r *inboxMultiplexer) DelayedMessagesRead() uint64 {
	return r.delayedMessagesRead
}
