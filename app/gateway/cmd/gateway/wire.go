//go:build wireinject
// +build wireinject

package main

import (
	"github.com/go-kratos/kratos/v2"
	"github.com/google/wire"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"github.com/osije/ai-os/app/gateway/internal/server"
	"github.com/osije/ai-os/app/gateway/internal/service"
)

func initApp() (*kratos.App, error) {
	wire.Build(
		conf.Load,
		service.NewGatewayServiceImpl,
		server.NewHTTPServer,
		newApp,
	)
	return nil, nil
}
