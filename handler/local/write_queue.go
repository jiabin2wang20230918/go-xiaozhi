package local

import (
	"errors"
	"time"

	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

// writeSeqUnscoped marks queue items that must always be delivered (control, stop, hello, …).
const writeSeqUnscoped = ^uint64(0)

const (
	writeQueueBackpressureReserve   = 32
	writeEnqueueCongestionTimeout   = 50 * time.Millisecond
)

type queuedWrite struct {
	speechSeq uint64
	payload   any
}

func unwrapQueuedWrite(event any) (speechSeq uint64, payload any) {
	if qw, ok := event.(queuedWrite); ok {
		return qw.speechSeq, qw.payload
	}
	return writeSeqUnscoped, event
}

func pullWritePayload(event any) any {
	_, payload := unwrapQueuedWrite(event)
	return payload
}

func isStaleSpeechPayload(payload any) bool {
	if _, ok := payload.([]byte); ok {
		return true
	}
	tts, ok := payload.(*xiaozhiapi.ServerEventTTS)
	if !ok {
		return false
	}
	switch tts.State {
	case xiaozhiapi.ServerTTSStateSentenceStart, xiaozhiapi.ServerTTSStateSentenceEnd:
		return true
	default:
		return false
	}
}

// shouldDeliverQueuedWrite implements speechSeq filtering in WriteLoop (Option A).
func shouldDeliverQueuedWrite(speechSeq uint64, payload any, currentSpeechSeq uint64) bool {
	if speechSeq == writeSeqUnscoped || speechSeq == currentSpeechSeq {
		return true
	}
	return !isStaleSpeechPayload(payload)
}

func isOpusBinaryPayload(payload any) bool {
	_, ok := payload.([]byte)
	return ok
}

func writeQueueBackpressureHighWatermark(capacity int) int {
	if capacity <= writeQueueBackpressureReserve {
		return capacity
	}
	return capacity - writeQueueBackpressureReserve
}

// reclaimWriteQRoom drops oldest Opus/binary frames when the queue is near full so control
// events can still be enqueued without starving behind stale audio.
func (h *Handler) reclaimWriteQRoom() {
	watermark := writeQueueBackpressureHighWatermark(cap(h.writeQ))
	for len(h.writeQ) >= watermark {
		select {
		case old := <-h.writeQ:
			if isOpusBinaryPayload(pullWritePayload(old)) {
				continue
			}
			select {
			case h.writeQ <- old:
			default:
				select {
				case h.writeQ <- old:
				case <-h.ctx.Done():
				}
			}
		default:
			return
		}
	}
}

func (h *Handler) tryEnqueueWrite(item queuedWrite) (err error) {
	payload := item.payload
	isBinary := isOpusBinaryPayload(payload)
	if !isBinary {
		h.reclaimWriteQRoom()
	}
	select {
	case h.writeQ <- item:
		if h.testAfterWrite != nil {
			h.testAfterWrite(payload)
		}
		return nil
	default:
	}
	if isBinary {
		h.reclaimWriteQRoom()
		select {
		case h.writeQ <- item:
			if h.testAfterWrite != nil {
				h.testAfterWrite(payload)
			}
			return nil
		default:
			return nil
		}
	}
	h.reclaimWriteQRoom()
	select {
	case h.writeQ <- item:
		if h.testAfterWrite != nil {
			h.testAfterWrite(payload)
		}
		return nil
	case <-time.After(writeEnqueueCongestionTimeout):
		return errors.New("write queue congested")
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}
