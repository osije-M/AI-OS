package main

import (
	"log"

	"github.com/go-kratos/kratos/v2"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

func newApp(hs *khttp.Server) *kratos.App {
	return kratos.New(
		kratos.Name("gateway"),
		kratos.Server(hs),
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
