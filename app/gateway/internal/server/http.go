package server

import (
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"github.com/osije/ai-os/app/gateway/internal/service"
)

func NewHTTPServer(cfg *conf.Config, svc *service.GatewayServiceImpl) *khttp.Server {
	s := khttp.NewServer(
		khttp.Address(cfg.HTTPAddr),
		// Kratos 默认每请求超时 1s；下游 LLM 调用慢，放宽到 120s。
		khttp.Timeout(120*time.Second),
	)
	r := s.Route("/")
	r.POST("/v1/run", svc.HandleRun)
	r.GET("/v1/trace/{id}", svc.HandleGetTrace)
	r.GET("/v1/traces", svc.HandleListTraces)
	r.GET("/viewer", svc.HandleViewer)
	return s
}
