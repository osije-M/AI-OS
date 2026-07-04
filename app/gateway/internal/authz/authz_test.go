package authz

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeAuthYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// chain 复刻 server 的接线：限流(外) → 鉴权(内) → 业务 200。
func chain(g *Guard) http.Handler {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return g.RateLimitFilter()(g.AuthFilter()(final))
}

func req(h http.Handler, method, path string, headers map[string]string) int {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestAuthDisabledAllowsAll(t *testing.T) {
	g := Load(writeAuthYAML(t, "version: v1\napi_keys: []\nrate_limit:\n  rps: 100\n  burst: 100\n"))
	if g.authEnabled {
		t.Fatal("auth should be disabled with empty api_keys")
	}
	if code := req(chain(g), "POST", "/v1/run", nil); code != http.StatusOK {
		t.Fatalf("auth disabled: want 200, got %d", code)
	}
}

func TestAuthEnabled(t *testing.T) {
	g := Load(writeAuthYAML(t, "version: v1\napi_keys:\n  - key: secret123\n    name: t\nrate_limit:\n  rps: 100\n  burst: 100\n"))
	if !g.authEnabled {
		t.Fatal("auth should be enabled when a key is configured")
	}
	h := chain(g)

	cases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		{"valid Bearer", "POST", "/v1/run", map[string]string{"Authorization": "Bearer secret123"}, http.StatusOK},
		{"valid X-API-Key", "POST", "/v1/run", map[string]string{"X-API-Key": "secret123"}, http.StatusOK},
		{"invalid key", "POST", "/v1/run", map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized},
		{"missing key", "POST", "/v1/run", nil, http.StatusUnauthorized},
		{"static /chat exempt", "GET", "/chat", nil, http.StatusOK},
		{"static /viewer exempt", "GET", "/viewer", nil, http.StatusOK},
	}
	for _, tt := range cases {
		if code := req(h, tt.method, tt.path, tt.headers); code != tt.want {
			t.Errorf("%s: want %d, got %d", tt.name, tt.want, code)
		}
	}
}

func TestRateLimit429(t *testing.T) {
	// rps=1 burst=2 → 同一 IP 前 2 个放行，第 3 个 429。
	g := Load(writeAuthYAML(t, "version: v1\napi_keys: []\nrate_limit:\n  rps: 1\n  burst: 2\n"))
	h := chain(g)
	c1 := req(h, "POST", "/v1/run", nil)
	c2 := req(h, "POST", "/v1/run", nil)
	c3 := req(h, "POST", "/v1/run", nil)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("first two want 200, got %d %d", c1, c2)
	}
	if c3 != http.StatusTooManyRequests {
		t.Fatalf("third want 429, got %d", c3)
	}
}

func TestLoadMissingFileSafeDegrade(t *testing.T) {
	// 配置缺失应安全降级为鉴权关、默认限流，且不 panic。
	g := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if g.authEnabled {
		t.Fatal("missing config: auth should be disabled")
	}
	if code := req(chain(g), "POST", "/v1/run", nil); code != http.StatusOK {
		t.Fatalf("missing config: want 200, got %d", code)
	}
}
