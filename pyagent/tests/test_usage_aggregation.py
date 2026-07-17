"""
M6-C② token usage aggregation tests.

Covers:
(a) offline run_graph() -> prompt_tokens=0, completion_tokens=0 (fixed contract value).
(b) with a fake DEEPSEEK_API_KEY and graph._call_llm monkeypatched to return fixed
    usage, run_graph() sums usage across every LLM call in the path: router
    (_llm_route) + worker (_run_worker) + reflect (reflect_node).
(c) run_graph_stream (streaming path) also accumulates router + worker usage into
    the "done" event's prompt_tokens/completion_tokens.
(d) graph._stream_llm captures real usage from the final chunk's `.usage` when
    the backend honors stream_options={"include_usage": True}.
(e) graph._stream_llm falls back to prompt_tokens=0, completion_tokens=<token
    event count> when the backend/SDK rejects stream_options (raises on create()).

No real network calls anywhere: the OpenAI client is faked at the `openai.OpenAI`
symbol (used via a local import inside graph._stream_llm), and graph._call_llm is
monkeypatched directly for the non-streaming aggregation tests.
"""
import types

import openai

import graph


# ---------------------------------------------------------------------------
# (a) offline
# ---------------------------------------------------------------------------

def test_run_graph_offline_usage_is_zero(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)

    result = graph.run_graph("what is the capital of France")

    assert result["prompt_tokens"] == 0
    assert result["completion_tokens"] == 0


# ---------------------------------------------------------------------------
# (b) unary aggregation across router + worker + reflect
# ---------------------------------------------------------------------------

def test_run_graph_sums_usage_across_router_worker_reflect(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    monkeypatch.setattr(graph, "MAX_LOOPS", 1)

    call_count = {"n": 0}

    def fake_call_llm(task, system_prompt="", timeout_override=None, temperature=None, **kwargs):
        call_count["n"] += 1
        return {
            "output": "research",  # valid route label AND not "RETRY..." for reflect
            "latency_ms": 1,
            "ok": True,
            "model_used": "fake-model",
            "attempts": 1,
            "events": [],
            "prompt_tokens": 10,
            "completion_tokens": 5,
        }

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)

    result = graph.run_graph("please explain something")

    # 3 LLM calls: supervisor routing, research worker, reflect judge.
    assert call_count["n"] == 3
    assert result["prompt_tokens"] == 30
    assert result["completion_tokens"] == 15
    assert result["route"] == "research"
    assert result["status"] == "OK"


def test_llm_route_returns_usage_tuple(monkeypatch):
    def fake_call_llm(task, system_prompt="", timeout_override=None, temperature=None, **kwargs):
        return {
            "output": "coding",
            "latency_ms": 1,
            "ok": True,
            "model_used": "fake-model",
            "attempts": 1,
            "events": [],
            "prompt_tokens": 7,
            "completion_tokens": 3,
        }

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)

    route, prompt_tokens, completion_tokens = graph._llm_route("write a function")

    assert route == "coding"
    assert prompt_tokens == 7
    assert completion_tokens == 3


def test_llm_route_audit_fast_path_reports_zero_usage(monkeypatch):
    # Audit keyword fast-path bypasses the LLM call entirely -> no tokens spent.
    def fake_call_llm(*a, **kw):
        raise AssertionError("audit fast-path must not call the LLM")

    monkeypatch.setattr(graph, "_call_llm", fake_call_llm)

    route, prompt_tokens, completion_tokens = graph._llm_route("pragma solidity ^0.8.0\ncontract Foo {}")

    assert route == "audit"
    assert prompt_tokens == 0
    assert completion_tokens == 0


# ---------------------------------------------------------------------------
# (c) streaming path aggregation
# ---------------------------------------------------------------------------

def test_run_graph_stream_done_event_sums_router_and_worker_usage(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")

    def fake_llm_route(task):
        return "research", 8, 2  # (route, prompt_tokens, completion_tokens)

    monkeypatch.setattr(graph, "_llm_route", fake_llm_route)

    def fake_stream_llm(task, system_prompt, node_name, collector, **kwargs):
        collector.append({
            "output": "hello world",
            "ok": True,
            "prompt_tokens": 11,
            "completion_tokens": 6,
        })
        return iter([{"type": "token", "node": node_name, "content": "hello world"}])

    monkeypatch.setattr(graph, "_stream_llm", fake_stream_llm)

    events = list(graph.run_graph_stream("please explain something"))
    done = events[-1]

    assert done["type"] == "done"
    assert done["prompt_tokens"] == 8 + 11
    assert done["completion_tokens"] == 2 + 6


def test_run_graph_stream_offline_usage_is_zero(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)

    events = list(graph.run_graph_stream("what is python"))
    done = events[-1]

    assert done["type"] == "done"
    assert done["prompt_tokens"] == 0
    assert done["completion_tokens"] == 0


# ---------------------------------------------------------------------------
# Fakes for the raw OpenAI streaming client (used by graph._stream_llm directly)
# ---------------------------------------------------------------------------

class _FakeChoice:
    def __init__(self, content):
        self.delta = types.SimpleNamespace(content=content)


class _FakeUsage:
    def __init__(self, prompt_tokens, completion_tokens):
        self.prompt_tokens = prompt_tokens
        self.completion_tokens = completion_tokens


class _FakeChunk:
    def __init__(self, content=None, usage=None):
        self.choices = [_FakeChoice(content)] if content is not None else []
        self.usage = usage


class _FakeCompletions:
    """Records create() kwargs; optionally rejects stream_options once."""

    def __init__(self, chunks, reject_stream_options=False):
        self._chunks = chunks
        self._reject_stream_options = reject_stream_options
        self.calls = []

    def create(self, **kwargs):
        self.calls.append(kwargs)
        if self._reject_stream_options and "stream_options" in kwargs:
            raise TypeError("stream_options is not a supported parameter")
        return iter(self._chunks)


class _FakeChat:
    def __init__(self, completions):
        self.completions = completions


class _FakeOpenAIClient:
    def __init__(self, completions):
        self.chat = _FakeChat(completions)

    def __call__(self, **kwargs):
        return self


# ---------------------------------------------------------------------------
# (d) real usage captured from the final streamed chunk
# ---------------------------------------------------------------------------

def test_stream_llm_captures_usage_from_final_chunk(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    chunks = [
        _FakeChunk(content="hel"),
        _FakeChunk(content="lo"),
        _FakeChunk(content=None, usage=_FakeUsage(12, 34)),
    ]
    completions = _FakeCompletions(chunks)
    monkeypatch.setattr(openai, "OpenAI", lambda **kw: _FakeOpenAIClient(completions))

    collector = []
    events = list(graph._stream_llm("hi", "", "research", collector))

    assert "".join(e["content"] for e in events) == "hello"
    assert collector[0]["ok"] is True
    assert collector[0]["prompt_tokens"] == 12
    assert collector[0]["completion_tokens"] == 34
    # include_usage was requested on the (successful) first attempt
    assert completions.calls[0].get("stream_options") == {"include_usage": True}


# ---------------------------------------------------------------------------
# (e) fallback to token-event-count when stream_options is rejected / no usage seen
# ---------------------------------------------------------------------------

def test_stream_llm_falls_back_to_token_count_when_stream_options_rejected(monkeypatch):
    monkeypatch.setenv("DEEPSEEK_API_KEY", "fake-key-for-test")
    chunks = [_FakeChunk(content="a"), _FakeChunk(content="b"), _FakeChunk(content="c")]
    completions = _FakeCompletions(chunks, reject_stream_options=True)
    monkeypatch.setattr(openai, "OpenAI", lambda **kw: _FakeOpenAIClient(completions))

    collector = []
    events = list(graph._stream_llm("hi", "", "research", collector))

    assert "".join(e["content"] for e in events) == "abc"
    assert collector[0]["ok"] is True
    assert collector[0]["prompt_tokens"] == 0
    assert collector[0]["completion_tokens"] == 3  # 3 token events, no usage chunk ever seen
    # first call attempted stream_options and was rejected, second call omitted it
    assert completions.calls[0].get("stream_options") == {"include_usage": True}
    assert "stream_options" not in completions.calls[1]
