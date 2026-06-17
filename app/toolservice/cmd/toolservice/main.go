package main

import (
	"log"

	"github.com/go-kratos/kratos/v2"
	ktgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
)

func newApp(gs *ktgrpc.Server) *kratos.App {
	return kratos.New(
		kratos.Name("toolservice"),
		kratos.Server(gs),
	)
}

func main() {
	app, err := initApp()
	if err != nil {
		log.Fatalf("failed to init app: %v", err)
	}
	if err := app.Run(); err != nil {
		log.Fatalf("app run error: %v", err)
	}
}
