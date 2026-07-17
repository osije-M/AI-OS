// Package metrics 提供 orchestrator 的运行时观测指标(M6-C②)。
//
// 业务指标只放在 orchestrator：它同时看得到 route、status、policy 决策和 LLM usage，
// gateway 层看不到 route，不值得为此引入 OTel 全家桶。
//
// /metrics 用独立的 net/http + promhttp 起在 METRICS_ADDR(默认 :9301)，不进 Kratos
// 的 gRPC server —— 避免为一个观测端口去动框架的传输层配置。生命周期由
// cmd/orchestrator 里的 kratos.BeforeStart/AfterStop 钩子管理（见 wire_gen.go / main.go）。
package metrics

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the orchestrator's Prometheus collectors, isolated in their own
// registry so /metrics only exposes the 5 aios_* series (no Go runtime noise
// from the default global registry, keeping cardinality/label review simple).
type Metrics struct {
	RequestsTotal   *prometheus.CounterVec   // aios_requests_total{route,status}
	RequestDuration *prometheus.HistogramVec // aios_request_duration_seconds{route}
	PolicyDenials   prometheus.Counter       // aios_policy_denials_total
	LLMTokensTotal  *prometheus.CounterVec   // aios_llm_tokens_total{route,kind="prompt"|"completion"}
	LLMCostUSDTotal *prometheus.CounterVec   // aios_llm_cost_usd_total{route}
	ActiveRuns      prometheus.Gauge         // aios_active_runs（M7-2 live 注册表 RUNNING 数）

	promptPriceUSDPer1M     float64
	completionPriceUSDPer1M float64

	registry *prometheus.Registry
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// New builds a fresh Metrics instance with its own registry.
// Price envs (估算用，可改): LLM_PRICE_PROMPT_USD_PER_1M (默认 0.27，deepseek-chat 牌价)，
// LLM_PRICE_COMPLETION_USD_PER_1M (默认 1.10)。
func New() *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	return &Metrics{
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "aios_requests_total",
			Help: "Total orchestrator requests, labeled by route and status (unary+stream both counted; deny -> route=\"\").",
		}, []string{"route", "status"}),
		RequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aios_request_duration_seconds",
			Help:    "Orchestrator request duration in seconds, labeled by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		PolicyDenials: factory.NewCounter(prometheus.CounterOpts{
			Name: "aios_policy_denials_total",
			Help: "Total requests denied by policy.",
		}),
		LLMTokensTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "aios_llm_tokens_total",
			Help: "Total LLM tokens consumed, labeled by route and kind (prompt|completion).",
		}, []string{"route", "kind"}),
		LLMCostUSDTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "aios_llm_cost_usd_total",
			Help: "Estimated LLM cost in USD, labeled by route (price(env) * tokens; estimate, not billing-accurate).",
		}, []string{"route"}),
		ActiveRuns: factory.NewGauge(prometheus.GaugeOpts{
			Name: "aios_active_runs",
			Help: "Number of runs currently in RUNNING state in the live registry (M7-2).",
		}),
		promptPriceUSDPer1M:     envFloat("LLM_PRICE_PROMPT_USD_PER_1M", 0.27),
		completionPriceUSDPer1M: envFloat("LLM_PRICE_COMPLETION_USD_PER_1M", 1.10),
		registry:                reg,
	}
}

// RecordRequest records one completed request (unary or stream) with its route,
// final status, and elapsed time in milliseconds.
func (m *Metrics) RecordRequest(route, status string, elapsedMs int64) {
	m.RequestsTotal.WithLabelValues(route, status).Inc()
	m.RequestDuration.WithLabelValues(route).Observe(float64(elapsedMs) / 1000.0)
}

// RecordPolicyDenial records one policy-denied request.
func (m *Metrics) RecordPolicyDenial() {
	m.PolicyDenials.Inc()
}

// RecordUsage records LLM token usage and its estimated USD cost for one
// successful request. Called only when a real RunGraphReply came back from
// AgentRuntime (offline path reports 0/0, which is still recorded — a zero
// data point is a legitimate observation, not "no observation").
func (m *Metrics) RecordUsage(route string, promptTokens, completionTokens int32) {
	m.LLMTokensTotal.WithLabelValues(route, "prompt").Add(float64(promptTokens))
	m.LLMTokensTotal.WithLabelValues(route, "completion").Add(float64(completionTokens))

	cost := float64(promptTokens)/1_000_000*m.promptPriceUSDPer1M +
		float64(completionTokens)/1_000_000*m.completionPriceUSDPer1M
	m.LLMCostUSDTotal.WithLabelValues(route).Add(cost)
}

// Serve starts the /metrics HTTP server on addr in a background goroutine and
// returns the *http.Server so the caller can Shutdown it during app teardown.
// Non-blocking: a listen error is logged, not returned, so it never blocks
// Kratos app startup (BeforeStart hooks must return promptly).
func (m *Metrics) Serve(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[metrics] /metrics server error on %s: %v", addr, err)
		}
	}()

	return srv
}

// Shutdown gracefully stops the /metrics server. Safe to call with a nil srv.
func Shutdown(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
