"""
M7-1 路由能力档案测试。

覆盖:档案加载(真实 configs/route-profiles.yaml)、缺失降级、
_call_llm 对 model/temperature/max_tokens 的实际传参(fake OpenAI 客户端)、
_run_worker 按 route 应用 profile 的接线。
"""
from types import SimpleNamespace

import graph


# ---------------------------------------------------------------------------
# 加载与降级
# ---------------------------------------------------------------------------

def test_profiles_loaded_from_repo_config():
    profiles = graph._load_route_profiles()
    # 与 configs/route-profiles.yaml 保持一致
    assert profiles["research"]["temperature"] == 0.7
    assert profiles["research"]["max_tokens"] == 2000
    assert profiles["coding"]["temperature"] == 0.2
    assert profiles["coding"]["max_tokens"] == 4000
    assert profiles["review"]["temperature"] == 0.0
    assert profiles["audit"] == {}


def test_missing_file_degrades_to_empty():
    assert graph._load_route_profiles(path="does/not/exist.yaml") == {}


def test_get_route_profile_unknown_route_returns_empty():
    assert graph._get_route_profile("no-such-route") == {}


# ---------------------------------------------------------------------------
# _call_llm 实际传参(fake OpenAI,不打网络)
# ---------------------------------------------------------------------------

class _FakeOpenAI:
    """Captures chat.completions.create kwargs into the shared dict."""

    captured: dict = {}

    def __init__(self, **_kwargs):
        self.chat = SimpleNamespace(
            completions=SimpleNamespace(create=self._create)
        )

    def _create(self, **kwargs):
        _FakeOpenAI.captured = kwargs
        return SimpleNamespace(
            choices=[SimpleNamespace(message=SimpleNamespace(content="fake answer"))],
            usage=SimpleNamespace(prompt_tokens=3, completion_tokens=5),
        )


def _call_with_fake(monkeypatch, **call_kwargs):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "test-key")
    monkeypatch.setattr("openai.OpenAI", _FakeOpenAI)
    _FakeOpenAI.captured = {}
    res = graph._call_llm("hello", **call_kwargs)
    assert res["ok"] is True
    return _FakeOpenAI.captured


def test_call_llm_applies_profile_params(monkeypatch):
    captured = _call_with_fake(
        monkeypatch,
        temperature=0.2, model_override="model-x", max_tokens=1234,
    )
    assert captured["model"] == "model-x"
    assert captured["temperature"] == 0.2
    assert captured["max_tokens"] == 1234


def test_call_llm_defaults_unchanged(monkeypatch):
    """无 profile 参数时保持既有行为:env 模型、max_tokens=512、不传 temperature。"""
    monkeypatch.setenv("DEEPSEEK_MODEL", "deepseek-chat")
    captured = _call_with_fake(monkeypatch)
    assert captured["model"] == "deepseek-chat"
    assert captured["max_tokens"] == 512
    assert "temperature" not in captured


# ---------------------------------------------------------------------------
# _run_worker 接线:按 route 取 profile 并传给 _call_llm
# ---------------------------------------------------------------------------

def test_run_worker_passes_route_profile(monkeypatch):
    seen = {}

    def fake_call_llm(task, system_prompt="", timeout_override=None,
                      temperature=None, model_override=None, max_tokens=None):
        seen.update(temperature=temperature, model_override=model_override,
                    max_tokens=max_tokens)
        return {
            "output": "ok", "latency_ms": 1, "ok": True, "model_used": "fake",
            "attempts": 1, "events": [],
            "prompt_tokens": 0, "completion_tokens": 0,
        }

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)
    monkeypatch.setattr(graph, "_call_tool_reverse", lambda *_a, **_k: ("", None))
    monkeypatch.setattr(graph, "_ROUTE_PROFILES",
                        {"coding": {"temperature": 0.2, "max_tokens": 4000, "model": "m-c"}})

    state = {"task": "write code", "trace": [], "route": "coding",
             "output": "", "status": "", "loops": 0,
             "prompt_tokens": 0, "completion_tokens": 0}
    graph._run_worker(state, "coding")

    assert seen == {"temperature": 0.2, "model_override": "m-c", "max_tokens": 4000}
