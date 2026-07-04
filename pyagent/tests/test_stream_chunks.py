"""
Tests for graph.run_graph_stream in fully offline mode (no DEEPSEEK_API_KEY).

Verifies the event sequence: node(supervisor) -> token(s) chunked by 4 chars
of "[offline] echo: {task}" -> done(status=OK, route=<classified>, output=full text).

No network calls, no DEEPSEEK_API_KEY, no gRPC/gen/ dependency exercised
(research/coding/review routes never call ToolService; only audit route would).
"""
import graph


def test_offline_stream_event_sequence(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)

    task = "what is Python"
    expected_route = graph._keyword_route(task)
    assert expected_route == "research"  # sanity check on the classifier's actual output

    events = list(graph.run_graph_stream(task))

    assert len(events) >= 3
    first = events[0]
    assert first["type"] == "node"
    assert first["node"] == "supervisor"

    token_events = events[1:-1]
    assert len(token_events) > 0
    for ev in token_events:
        assert ev["type"] == "token"

    expected_full_text = f"[offline] echo: {task}"
    concatenated = "".join(ev["content"] for ev in token_events)
    assert concatenated == expected_full_text

    last = events[-1]
    assert last["type"] == "done"
    assert last["status"] == "OK"
    assert last["route"] == expected_route
    assert last["output"] == expected_full_text


def test_offline_stream_chunk_size_is_four_chars():
    task = "hi"
    monkeypatch_removed = False  # no monkeypatch fixture needed; DEEPSEEK_API_KEY absence assumed by env
    events = list(graph.run_graph_stream(task))
    token_events = [ev for ev in events if ev["type"] == "token"]

    expected_full_text = f"[offline] echo: {task}"
    # All chunks except possibly the last should be exactly 4 chars.
    for ev in token_events[:-1]:
        assert len(ev["content"]) == 4
    assert len(token_events[-1]["content"]) <= 4
    assert "".join(ev["content"] for ev in token_events) == expected_full_text
