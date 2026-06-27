package server

import (
	kgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
	"github.com/osije/ai-os/app/tracestore/internal/service"
)

func NewGRPCServer(cfg *conf.Config, svc *service.TraceStoreServiceImpl) *kgrpc.Server {
	s := kgrpc.NewServer(
		kgrpc.Address(cfg.GRPCAddr),
	)
	tracev1.RegisterTraceStoreServer(s, svc)
	return s
}
