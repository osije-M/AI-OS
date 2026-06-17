package server

import (
	"github.com/go-kratos/kratos/v2/transport/grpc"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"github.com/osije/ai-os/app/orchestrator/internal/service"
)

func NewGRPCServer(cfg *conf.Config, svc *service.OrchestratorServiceImpl) *grpc.Server {
	s := grpc.NewServer(
		grpc.Address(cfg.GRPCAddr),
	)
	orchestratorv1.RegisterOrchestratorServer(s, svc)
	return s
}
