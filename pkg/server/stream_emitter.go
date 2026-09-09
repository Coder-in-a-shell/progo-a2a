package server

import (
	"sync/atomic"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

type errorTrackingEmitter struct {
	stream.Emitter
	errorEmitted atomic.Bool
}

func (e *errorTrackingEmitter) Emit(eventType model.StreamEventType, data any) error {
	if eventType == model.EventTaskError {
		e.errorEmitted.Store(true)
	}
	return e.Emitter.Emit(eventType, data)
}
