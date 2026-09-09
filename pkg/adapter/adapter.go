package adapter

import (
	"context"
	"net/http"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

type Adapter interface {
	Type() string
	TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error)
	TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error)
	TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error
}
