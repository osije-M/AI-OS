package server

import (
	"github.com/go-kratos/kratos/v2/transport/grpc"
	toolv1 "github.com/osije/ai-os/api/gen/go/tool/v1"
	"github.com/osije/ai-os/app/toolservice/internal/conf"
	"github.com/osije/ai-os/app/toolservice/internal/service"
)

func NewGRPCServer(cfg *conf.Config, svc *service.ToolServiceImpl) *grpc.Server {
	s := grpc.NewServer(
		grpc.Address(cfg.GRPCAddr),
	)
	toolv1.RegisterToolServiceServer(s, svc)
	return s
}
