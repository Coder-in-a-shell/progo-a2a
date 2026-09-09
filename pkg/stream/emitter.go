package stream

import "github.com/Coder-in-a-shell/progo-a2a/pkg/model"

type Emitter interface {
	Emit(eventType model.StreamEventType, data any) error
}
