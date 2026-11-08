package local

import xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"

// writeSeqUnscoped marks queue items that must always be delivered (control, stop, hello, …).
const writeSeqUnscoped = ^uint64(0)

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
