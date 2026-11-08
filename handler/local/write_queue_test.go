package local

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

func TestShouldDeliverQueuedWrite(t *testing.T) {
	stop := &xiaozhiapi.ServerEventTTS{State: xiaozhiapi.ServerTTSStateStop}
	start := &xiaozhiapi.ServerEventTTS{State: xiaozhiapi.ServerTTSStateSentenceStart}
	end := &xiaozhiapi.ServerEventTTS{State: xiaozhiapi.ServerTTSStateSentenceEnd}
	frame := []byte("audio")

	if !shouldDeliverQueuedWrite(writeSeqUnscoped, stop, 2) {
		t.Fatal("unscoped stop must always deliver")
	}
	if !shouldDeliverQueuedWrite(1, frame, 1) {
		t.Fatal("current-seq audio must deliver")
	}
	if shouldDeliverQueuedWrite(1, frame, 2) {
		t.Fatal("stale audio must drop")
	}
	if shouldDeliverQueuedWrite(1, start, 2) {
		t.Fatal("stale sentence_start must drop")
	}
	if shouldDeliverQueuedWrite(1, end, 2) {
		t.Fatal("stale sentence_end must drop")
	}
}

func TestSendSpeechSegmentCanceledSkipsSentenceEnd(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	pullWritePayload(<-h.Recv(context.Background()))

	seq := h.speechSeq.Load()
	var once sync.Once
	h.testAfterWrite = func(event any) {
		frame, ok := event.([]byte)
		if !ok || string(frame) != "only" {
			return
		}
		once.Do(func() { h.speechSeq.Add(1) })
	}

	if err := h.sendSpeechSegment(voice.SpeechSegment{
		Text:  "cancel mid",
		Audio: [][]byte{[]byte("only"), []byte("stale")},
	}, seq); err != nil {
		t.Fatalf("sendSpeechSegment: %v", err)
	}

	start := pullWritePayload(<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if start.State != xiaozhiapi.ServerTTSStateSentenceStart {
		t.Fatalf("expected sentence_start, got %s", start.State)
	}
	if got := string(pullWritePayload(<-h.Recv(context.Background())).([]byte)); got != "only" {
		t.Fatalf("unexpected frame: %q", got)
	}
	select {
	case raw := <-h.Recv(context.Background()):
		t.Fatalf("canceled segment must not emit sentence_end, got %T", pullWritePayload(raw))
	default:
	}
}

func TestWriteLoopDropsStaleAudioAfterAbortStop(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = h.Close(context.Background())
	})
	pullWritePayload(<-h.Recv(context.Background()))

	var mu sync.Mutex
	var binaryWrites [][]byte
	w := &ConnWrapper{
		ctx:     ctx,
		handler: h,
		done:    make(chan struct{}),
		writeMessageFunc: func(messageType int, data []byte) error {
			if messageType != websocket.BinaryMessage {
				return nil
			}
			mu.Lock()
			binaryWrites = append(binaryWrites, append([]byte(nil), data...))
			mu.Unlock()
			return nil
		},
	}
	go w.WriteLoop(ctx)

	seq := h.speechSeq.Load()
	h.speechSeq.Add(1)
	if err := h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{Type: xiaozhiapi.ServerEventTypeTTS, SessionId: h.sessionID},
		State:           xiaozhiapi.ServerTTSStateStop,
	}); err != nil {
		t.Fatalf("queue stop: %v", err)
	}
	// Simulate in-flight sendSpeechSegment still enqueueing after abort bumped speechSeq.
	if err := h.writeSpeech([]byte("stale"), seq); err != nil {
		t.Fatalf("queue stale: %v", err)
	}
	if err := h.writeSpeech(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{Type: xiaozhiapi.ServerEventTypeTTS, SessionId: h.sessionID},
		State:           xiaozhiapi.ServerTTSStateSentenceEnd,
		Text:            "old",
	}, seq); err != nil {
		t.Fatalf("queue stale end: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(binaryWrites) != 0 {
		t.Fatalf("stale audio must not reach device after stop, got %+v", binaryWrites)
	}
}

func TestReclaimWriteQRoomDropsOldestBinaryAdmitsControl(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	pullWritePayload(<-h.Recv(context.Background()))
	h.writeQ = make(chan any, 4)

	seq := h.speechSeq.Load()
	for i := 0; i < 4; i++ {
		if err := h.writeSpeech([]byte{byte(i)}, seq); err != nil {
			t.Fatalf("fill binary %d: %v", i, err)
		}
	}
	if len(h.writeQ) != 4 {
		t.Fatalf("expected full queue, len=%d", len(h.writeQ))
	}

	stop := &xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{Type: xiaozhiapi.ServerEventTypeTTS, SessionId: h.sessionID},
		State:           xiaozhiapi.ServerTTSStateStop,
	}
	if err := h.write(stop); err != nil {
		t.Fatalf("enqueue control under backpressure: %v", err)
	}

	foundStop := false
	for i := 0; i < 6; i++ {
		raw := <-h.Recv(context.Background())
		got := pullWritePayload(raw)
		if tts, ok := got.(*xiaozhiapi.ServerEventTTS); ok && tts.State == xiaozhiapi.ServerTTSStateStop {
			foundStop = true
			break
		}
	}
	if !foundStop {
		t.Fatal("expected tts stop after reclaim dropped oldest binary")
	}
	if len(h.writeQ) > 4 {
		t.Fatalf("queue length must not exceed capacity, len=%d", len(h.writeQ))
	}
}

func TestEnqueueWriteDropsNewBinaryWhenQueueSaturated(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	pullWritePayload(<-h.Recv(context.Background()))
	h.writeQ = make(chan any, 2)
	seq := h.speechSeq.Load()

	if err := h.writeSpeech([]byte("a"), seq); err != nil {
		t.Fatal(err)
	}
	if err := h.writeSpeech([]byte("b"), seq); err != nil {
		t.Fatal(err)
	}
	if err := h.writeSpeech([]byte("overflow"), seq); err != nil {
		t.Fatalf("saturated binary enqueue should drop, got err=%v", err)
	}
	if len(h.writeQ) != 2 {
		t.Fatalf("expected len 2 after drop, got %d", len(h.writeQ))
	}
	if err := h.write(&xiaozhiapi.ServerEventSTT{
		ServerEventBase: xiaozhiapi.ServerEventBase{Type: xiaozhiapi.ServerEventTypeSTT, SessionId: h.sessionID},
		Text:            "hi",
	}); err != nil {
		t.Fatalf("control must enqueue after reclaim: %v", err)
	}
}
