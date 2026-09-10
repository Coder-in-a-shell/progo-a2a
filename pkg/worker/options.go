package worker

import "log/slog"

// Option configures optional Engine settings.
type Option func(*Engine)

// WithLogger configures a structured logger for the engine.
func WithLogger(logger *slog.Logger) Option {
	return func(e *Engine) {
		if logger != nil {
			e.logger = logger
		}
	}
}
