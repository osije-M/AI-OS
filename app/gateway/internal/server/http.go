package server

import (
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	gatewayv1 "github.com/osije/ai-os/api/gen/go/gateway/v1"
	"github.com/osije/ai-os/app/gateway/internal/authz"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"github.com/osije/ai-os/app/gateway/internal/service"
)

func NewHTTPServer(cfg *conf.Config, svc *service.GatewayServiceImpl) *khttp.Server {
	// 横切关注点走 Filter（限流→鉴权），不侵入业务 handler。
	guard := authz.Load(cfg.AuthFile)
	s := khttp.NewServer(
		khttp.Address(cfg.HTTPAddr),
		// Kratos 默认每请求超时 1s；下游 LLM 调用慢，放宽到 120s。
		khttp.Timeout(120*time.Second),
		khttp.Filter(guard.RateLimitFilter(), guard.AuthFilter()),
	)
	// POST /v1/run 由 gateway.proto 的 google.api.http 注解生成并注册（契约先行）。
	gatewayv1.RegisterGatewayHTTPServer(s, svc)
	// trace 查询与 viewer 仍手写路由（gateway 代理 trace-store / 静态页，不入 gateway.proto）。
	r := s.Route("/")
	r.GET("/v1/trace/{id}", svc.HandleGetTrace)
	r.GET("/v1/traces", svc.HandleListTraces)
	r.GET("/v1/runs", svc.HandleListRuns)              // M7-2 live 注册表
	r.POST("/v1/run/{id}/cancel", svc.HandleCancelRun) // M7-3 取消在飞 run
	r.GET("/viewer", svc.HandleViewer)
	r.GET("/chat", svc.HandleStreamDemo)
	r.POST("/v1/run/stream", svc.HandleRunStream)
	return s
}
