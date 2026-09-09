package server

import (
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

type recordingEmitter struct {
	events []model.StreamEventType
}

func (r *recordingEmitter) Emit(eventType model.StreamEventType, _ any) error {
	r.events = append(r.events, eventType)
	return nil
}

func TestErrorTrackingEmitterTracksOnlyTaskErrors(t *testing.T) {
	recorder := &recordingEmitter{}
	emitter := &errorTrackingEmitter{Emitter: recorder}

	if err := emitter.Emit(model.EventTaskStarted, nil); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if emitter.errorEmitted.Load() {
		t.Fatal("task_started must not mark an error")
	}
	if err := emitter.Emit(model.EventTaskError, nil); err != nil {
		t.Fatalf("emit error: %v", err)
	}
	if !emitter.errorEmitted.Load() {
		t.Fatal("task_error must mark an error")
	}
}
