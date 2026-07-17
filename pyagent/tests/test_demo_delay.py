"""M7-2 测试钩子 demo_delay_ms 的安全性测试(仅离线生效、上限、非法值)。"""
import time

import graph


def test_delay_parsed_offline(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    assert graph._demo_delay_seconds({"demo_delay_ms": "1500"}) == 1.5


def test_delay_capped_at_30s(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    assert graph._demo_delay_seconds({"demo_delay_ms": "999999999"}) == 30.0


def test_delay_ignored_online(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "real-key")
    assert graph._demo_delay_seconds({"demo_delay_ms": "5000"}) == 0.0


def test_delay_invalid_or_absent(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    assert graph._demo_delay_seconds(None) == 0.0
    assert graph._demo_delay_seconds({}) == 0.0
    assert graph._demo_delay_seconds({"demo_delay_ms": "abc"}) == 0.0
    assert graph._demo_delay_seconds({"demo_delay_ms": "-5"}) == 0.0


def test_run_graph_offline_actually_delays(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    t0 = time.monotonic()
    result = graph.run_graph("hello", params={"demo_delay_ms": "200"})
    elapsed = time.monotonic() - t0
    assert result["status"] == "OK"
    assert elapsed >= 0.2
