package stream

import "a2a-proxy/pkg/model"

type Emitter interface {
	Emit(eventType model.StreamEventType, data any) error
}
