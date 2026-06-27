//go:build wireinject
// +build wireinject

package main

import (
	"github.com/go-kratos/kratos/v2"
	"github.com/google/wire"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
	"github.com/osije/ai-os/app/tracestore/internal/server"
	"github.com/osije/ai-os/app/tracestore/internal/service"
)

func initApp() (*kratos.App, error) {
	wire.Build(conf.Load, service.NewTraceStoreServiceImpl, server.NewGRPCServer, newApp)
	return nil, nil
}
