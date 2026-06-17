package server

import (
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"github.com/osije/ai-os/app/gateway/internal/service"
)

func NewHTTPServer(cfg *conf.Config, svc *service.GatewayServiceImpl) *khttp.Server {
	s := khttp.NewServer(
		khttp.Address(cfg.HTTPAddr),
	)
	r := s.Route("/")
	r.POST("/v1/run", svc.HandleRun)
	return s
}
