package main

import (
	"context"
	"log"
	"net/http"

	"github.com/go-kratos/kratos/v2"
	ktgrpc "github.com/go-kratos/kratos/v2/transport/grpc"

	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"github.com/osije/ai-os/app/orchestrator/internal/metrics"
)

func newApp(cfg *conf.Config, gs *ktgrpc.Server, m *metrics.Metrics) *kratos.App {
	// M6-C②：/metrics 独立 HTTP server（promhttp），随 Kratos app 生命周期启停。
	var metricsSrv *http.Server
	return kratos.New(
		kratos.Name("orchestrator"),
		kratos.Server(gs),
		kratos.BeforeStart(func(context.Context) error {
			metricsSrv = m.Serve(cfg.MetricsAddr)
			log.Printf("[orchestrator] metrics exposed at %s/metrics", cfg.MetricsAddr)
			return nil
		}),
		kratos.AfterStop(func(ctx context.Context) error {
			return metrics.Shutdown(ctx, metricsSrv)
		}),
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
