// Package authz 提供 gateway 的两项横切关注点：API Key 鉴权与令牌桶限流。
// 二者均以 Kratos HTTP Filter（标准 net/http 中间件）实现，作用于整个 HTTP 入口，
// 不侵入业务 handler。配置来自 YAML（configs/auth.yaml），与 policy 引擎同样的
// “读取/解析失败即安全降级”策略，保证 gateway 始终能启动。
package authz

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// APIKey 是一条已授权客户端凭据，可携带独立限流参数（留空则用全局默认）。
type APIKey struct {
	Key   string  `yaml:"key"`
	Name  string  `yaml:"name"`
	RPS   float64 `yaml:"rps"`   // 每秒令牌数；<=0 用全局默认
	Burst int     `yaml:"burst"` // 桶容量；<=0 用全局默认
}

type rateConfig struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
}

type configFile struct {
	Version   string     `yaml:"version"`
	APIKeys   []APIKey   `yaml:"api_keys"`
	RateLimit rateConfig `yaml:"rate_limit"`
}

const (
	defaultRPS   = 20
	defaultBurst = 40
	// authPathPrefix 之下的接口才强制鉴权；静态页(/viewer、/chat)放行。
	authPathPrefix = "/v1/"
)

// Guard 持有鉴权与限流状态，并发安全。
type Guard struct {
	authEnabled bool
	keys        map[string]APIKey // key 明文 -> APIKey
	defaultRate rateConfig

	mu       sync.Mutex
	limiters map[string]*rate.Limiter // clientID -> 令牌桶
}

// Load 读取 YAML 鉴权配置并返回 Guard。读取/解析失败时安全降级为
// “鉴权关闭 + 默认限流”，与 policy.Load 保持一致，保证 gateway 仍能启动。
func Load(path string) *Guard {
	g := &Guard{
		keys:        map[string]APIKey{},
		limiters:    map[string]*rate.Limiter{},
		defaultRate: rateConfig{RPS: defaultRPS, Burst: defaultBurst},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[authz] no auth config at %q (%v) — auth DISABLED, default rate %.0f rps", path, err, g.defaultRate.RPS)
		return g
	}
	var cf configFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		log.Printf("[authz] parse error %q: %v — auth DISABLED, default rate", path, err)
		return g
	}
	if cf.RateLimit.RPS > 0 {
		g.defaultRate.RPS = cf.RateLimit.RPS
	}
	if cf.RateLimit.Burst > 0 {
		g.defaultRate.Burst = cf.RateLimit.Burst
	}
	for _, k := range cf.APIKeys {
		if k.Key == "" {
			continue
		}
		g.keys[k.Key] = k
	}
	g.authEnabled = len(g.keys) > 0
	log.Printf("[authz] loaded %d api key(s) from %q (auth=%v, default rate %.0f rps burst %d)",
		len(g.keys), path, g.authEnabled, g.defaultRate.RPS, g.defaultRate.Burst)
	return g
}

// AuthFilter 仅对 authPathPrefix 下的接口强制 API Key 鉴权；鉴权关闭时全放行。
// 凭据来自 `Authorization: Bearer <key>` 或 `X-API-Key: <key>`。
func (g *Guard) AuthFilter() khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if g.authEnabled && strings.HasPrefix(r.URL.Path, authPathPrefix) {
				if _, ok := g.keys[extractKey(r)]; !ok {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized: missing or invalid API key")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitFilter 按客户端令牌桶限流：已识别的 key 用其独立速率，否则按客户端 IP 用默认速率。
func (g *Guard) RateLimitFilter() khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !g.limiterFor(r).Allow() {
				writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limiterFor 取（或惰性创建）该请求对应的令牌桶。
// 注：原型阶段 limiters 不做过期清理；生产化时可加 LRU/TTL 回收，避免 IP 过多时无界增长。
func (g *Guard) limiterFor(r *http.Request) *rate.Limiter {
	id := "ip:" + clientIP(r)
	rps, burst := g.defaultRate.RPS, g.defaultRate.Burst
	if key := extractKey(r); key != "" {
		if k, ok := g.keys[key]; ok {
			id = "key:" + k.Name
			if k.RPS > 0 {
				rps = k.RPS
			}
			if k.Burst > 0 {
				burst = k.Burst
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	lim, ok := g.limiters[id]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(rps), burst)
		g.limiters[id] = lim
	}
	return lim
}

func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "error": msg})
}
