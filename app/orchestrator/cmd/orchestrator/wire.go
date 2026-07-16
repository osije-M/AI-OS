//go:build wireinject
// +build wireinject

package main

import (
	"github.com/go-kratos/kratos/v2"
	"github.com/google/wire"
	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"github.com/osije/ai-os/app/orchestrator/internal/metrics"
	"github.com/osije/ai-os/app/orchestrator/internal/server"
	"github.com/osije/ai-os/app/orchestrator/internal/service"
)

func initApp() (*kratos.App, error) {
	wire.Build(
		conf.Load,
		metrics.New,
		service.NewOrchestratorServiceImpl,
		server.NewGRPCServer,
		newApp,
	)
	return nil, nil
}
