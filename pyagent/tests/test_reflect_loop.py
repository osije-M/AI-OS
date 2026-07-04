"""
Tests for graph.reflect_node bounded-loop behavior.

(a) no DEEPSEEK_API_KEY -> always PASS, loop_count unchanged.
(b) API key set + loop_count >= MAX_LOOPS -> forced PASS (loop cap).
(c) API key set + loop_count < MAX_LOOPS + _call_llm returns RETRY -> verdict RETRY, loop_count += 1.
(d) API key set + _call_llm returns ok=False -> conservative PASS.

None of these make real network calls: _call_llm is monkeypatched wherever the
API key path is exercised.
"""
import graph


def _base_state(loop_count: int = 0) -> dict:
    return {
        "task": "what is python",
        "route": "research",
        "output": "Python is a programming language.",
        "trace": [],
        "llm_ok": True,
        "loop_count": loop_count,
    }


def test_reflect_offline_always_pass(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)

    state = _base_state(loop_count=3)  # even with a high loop_count, offline never loops
    result = graph.reflect_node(state)

    assert result["loop_count"] == 3
    last_trace = result["trace"][-1]
    assert last_trace["node"] == "reflect"
    assert "PASS (offline, fixed)" in last_trace["summary"]


def test_reflect_forced_pass_at_max_loops(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    monkeypatch.setattr(graph, "MAX_LOOPS", 1)

    state = _base_state(loop_count=1)  # loop_count >= MAX_LOOPS
    result = graph.reflect_node(state)

    assert result["loop_count"] == 1  # unchanged: forced PASS doesn't increment
    last_trace = result["trace"][-1]
    assert "PASS" in last_trace["summary"]
    assert "max_loops=1" in last_trace["summary"]


def test_reflect_retry_when_llm_says_retry(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    monkeypatch.setattr(graph, "MAX_LOOPS", 3)

    def fake_call_llm(task, system_prompt="", timeout_override=None):
        return {
            "output": "RETRY: needs more detail",
            "latency_ms": 5,
            "ok": True,
            "model_used": "fake-model",
            "attempts": 1,
            "events": [],
        }

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)

    state = _base_state(loop_count=0)  # loop_count < MAX_LOOPS
    result = graph.reflect_node(state)

    assert result["loop_count"] == 1  # incremented
    last_trace = result["trace"][-1]
    assert "-> RETRY" in last_trace["summary"]


def test_reflect_conservative_pass_when_llm_not_ok(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    monkeypatch.setattr(graph, "MAX_LOOPS", 3)

    def fake_call_llm(task, system_prompt="", timeout_override=None):
        return {
            "output": "",
            "latency_ms": 5,
            "ok": False,
            "model_used": "none",
            "attempts": 3,
            "events": [],
        }

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)

    state = _base_state(loop_count=0)
    result = graph.reflect_node(state)

    assert result["loop_count"] == 0  # not incremented, PASS conservative
    last_trace = result["trace"][-1]
    assert "PASS (reflect LLM failed, conservative)" in last_trace["summary"]
