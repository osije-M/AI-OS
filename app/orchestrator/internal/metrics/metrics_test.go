package metrics

import (
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordRequestAndDenial(t *testing.T) {
	m := New()

	m.RecordRequest("research", "OK", 120)
	m.RecordRequest("research", "OK", 80)
	m.RecordRequest("coding", "OK", 50)
	m.RecordRequest("", "DENIED", 3)
	m.RecordPolicyDenial()

	if got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("research", "OK")); got != 2 {
		t.Fatalf("requests_total{research,OK} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("coding", "OK")); got != 1 {
		t.Fatalf("requests_total{coding,OK} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("", "DENIED")); got != 1 {
		t.Fatalf("requests_total{'',DENIED} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.PolicyDenials); got != 1 {
		t.Fatalf("policy_denials_total = %v, want 1", got)
	}

	// histogram series：research / coding / ""(deny) 共 3 个（CollectAndCount 数的是 series 数）
	if got := testutil.CollectAndCount(m.RequestDuration); got != 3 {
		t.Fatalf("request_duration series = %v, want 3 (research, coding, deny='')", got)
	}
}

func TestRecordUsageAndCost(t *testing.T) {
	t.Setenv("LLM_PRICE_PROMPT_USD_PER_1M", "1.0")
	t.Setenv("LLM_PRICE_COMPLETION_USD_PER_1M", "2.0")
	m := New()

	m.RecordUsage("research", 1_000_000, 500_000)

	if got := testutil.ToFloat64(m.LLMTokensTotal.WithLabelValues("research", "prompt")); got != 1_000_000 {
		t.Fatalf("tokens{prompt} = %v, want 1000000", got)
	}
	if got := testutil.ToFloat64(m.LLMTokensTotal.WithLabelValues("research", "completion")); got != 500_000 {
		t.Fatalf("tokens{completion} = %v, want 500000", got)
	}
	// cost = 1.0M/1M*1.0 + 0.5M/1M*2.0 = 2.0
	if got := testutil.ToFloat64(m.LLMCostUSDTotal.WithLabelValues("research")); math.Abs(got-2.0) > 1e-9 {
		t.Fatalf("cost_usd = %v, want 2.0", got)
	}
}

func TestRecordUsageZeroIsRecorded(t *testing.T) {
	m := New()
	// offline 路径：0/0 也是合法观测点，series 应存在且为 0
	m.RecordUsage("coding", 0, 0)
	if got := testutil.ToFloat64(m.LLMTokensTotal.WithLabelValues("coding", "prompt")); got != 0 {
		t.Fatalf("tokens{coding,prompt} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.LLMCostUSDTotal.WithLabelValues("coding")); got != 0 {
		t.Fatalf("cost_usd{coding} = %v, want 0", got)
	}
}

func TestPriceEnvFallbackOnGarbage(t *testing.T) {
	t.Setenv("LLM_PRICE_PROMPT_USD_PER_1M", "not-a-number")
	m := New()
	if m.promptPriceUSDPer1M != 0.27 {
		t.Fatalf("promptPrice = %v, want default 0.27 on parse failure", m.promptPriceUSDPer1M)
	}
}
