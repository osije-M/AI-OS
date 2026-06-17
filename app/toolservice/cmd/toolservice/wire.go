//go:build wireinject
// +build wireinject

package main

import (
	"github.com/go-kratos/kratos/v2"
	"github.com/google/wire"
	"github.com/osije/ai-os/app/toolservice/internal/conf"
	"github.com/osije/ai-os/app/toolservice/internal/server"
	"github.com/osije/ai-os/app/toolservice/internal/service"
)

func initApp() (*kratos.App, error) {
	wire.Build(
		conf.Load,
		service.NewToolServiceImpl,
		server.NewGRPCServer,
		newApp,
	)
	return nil, nil
}
