package server

import (
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	gatewayv1 "github.com/osije/ai-os/api/gen/go/gateway/v1"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"github.com/osije/ai-os/app/gateway/internal/service"
)

func NewHTTPServer(cfg *conf.Config, svc *service.GatewayServiceImpl) *khttp.Server {
	s := khttp.NewServer(
		khttp.Address(cfg.HTTPAddr),
		// Kratos 默认每请求超时 1s；下游 LLM 调用慢，放宽到 120s。
		khttp.Timeout(120*time.Second),
	)
	// POST /v1/run 由 gateway.proto 的 google.api.http 注解生成并注册（契约先行）。
	gatewayv1.RegisterGatewayHTTPServer(s, svc)
	// trace 查询与 viewer 仍手写路由（gateway 代理 trace-store / 静态页，不入 gateway.proto）。
	r := s.Route("/")
	r.GET("/v1/trace/{id}", svc.HandleGetTrace)
	r.GET("/v1/traces", svc.HandleListTraces)
	r.GET("/viewer", svc.HandleViewer)
	r.POST("/v1/run/stream", svc.HandleRunStream)
	return s
}
