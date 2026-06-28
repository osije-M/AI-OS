"""
Smoke test for run_graph_stream (offline mode, no gRPC server needed).

Tests:
1. run_graph_stream offline: supervisor node event, multiple token events, done event
2. run_graph (unary) regression: still returns route/status correctly

Run with:
    cd pyagent
    PYTHONUTF8=1 DEEPSEEK_API_KEY="" pyagent/.venv/Scripts/python.exe smoke_test_stream.py

Or from repo root:
    PYTHONUTF8=1 DEEPSEEK_API_KEY="" pyagent/.venv/Scripts/python.exe pyagent/smoke_test_stream.py
"""

import os
import sys

# Ensure DEEPSEEK_API_KEY is empty for offline mode
os.environ["DEEPSEEK_API_KEY"] = ""

# Add gen dir to path (same as server.py does)
_DIR = os.path.dirname(os.path.abspath(__file__))
_GEN = os.path.join(_DIR, "gen")
if _GEN not in sys.path:
    sys.path.insert(0, _GEN)

# Add pyagent dir itself to sys.path so "from graph import ..." works
if _DIR not in sys.path:
    sys.path.insert(0, _DIR)

from graph import run_graph_stream, run_graph  # noqa: E402

# ---------------------------------------------------------------------------
# Test 1: run_graph_stream offline
# ---------------------------------------------------------------------------

print("=" * 60)
print("TEST 1: run_graph_stream (offline, no API key)")
print("=" * 60)

task = "explain how binary search works"
events = list(run_graph_stream(task, trace_id="smoke-001"))

print(f"Total events received: {len(events)}")
print()

for i, ev in enumerate(events):
    ev_type = ev.get("type")
    if ev_type == "node":
        print(f"  [{i}] NODE   node={ev['node']!r} content={ev['content']!r}")
    elif ev_type == "token":
        # Print content safely (avoid GBK issues on Windows console)
        content_repr = repr(ev["content"])
        print(f"  [{i}] TOKEN  node={ev['node']!r} content={content_repr}")
    elif ev_type == "done":
        output_len = len(ev.get("output", ""))
        trace_len = len(ev.get("trace", []))
        print(f"  [{i}] DONE   route={ev['route']!r} status={ev['status']!r} "
              f"output_len={output_len} trace_entries={trace_len}")
        print(f"        output_preview={repr(ev.get('output', '')[:60])}")
    elif ev_type == "error":
        print(f"  [{i}] ERROR  content={ev['content']!r}")
    else:
        print(f"  [{i}] UNKNOWN type={ev_type!r}")

print()

# --- Assertions ---
node_events = [e for e in events if e.get("type") == "node"]
token_events = [e for e in events if e.get("type") == "token"]
done_events  = [e for e in events if e.get("type") == "done"]
error_events = [e for e in events if e.get("type") == "error"]

assert len(node_events) >= 1, f"Expected at least 1 node event, got {len(node_events)}"
sup_events = [e for e in node_events if e.get("node") == "supervisor"]
assert len(sup_events) >= 1, "Expected supervisor node event"
print(f"[PASS] supervisor node event present ({len(sup_events)} found)")

assert len(token_events) > 1, (
    f"Expected multiple token events (streaming chunks), got {len(token_events)}. "
    "Offline mode should split text into 4-char chunks."
)
print(f"[PASS] multiple token events: {len(token_events)} chunks")

assert len(done_events) == 1, f"Expected exactly 1 done event, got {len(done_events)}"
done = done_events[0]
assert done.get("status") in ("OK", "FAILED"), f"Unexpected status: {done.get('status')}"
assert len(done.get("output", "")) > 0, "done event output should be non-empty"
# Reconstruct from tokens and compare
reconstructed = "".join(e["content"] for e in token_events)
assert reconstructed == done["output"], (
    f"Reconstructed output from tokens != done output\n"
    f"  tokens: {reconstructed!r}\n"
    f"  done:   {done['output']!r}"
)
print(f"[PASS] done event present, status={done['status']!r}, output_len={len(done['output'])}")
print(f"[PASS] reconstructed output from tokens matches done.output")

assert len(error_events) == 0, f"Unexpected error events: {error_events}"
print(f"[PASS] no error events")

# Check trace has at least supervisor + worker entries
trace = done.get("trace", [])
assert len(trace) >= 2, f"Expected at least 2 trace entries (supervisor + worker), got {len(trace)}"
trace_nodes = [t["node"] for t in trace]
assert "supervisor" in trace_nodes, f"'supervisor' not in trace nodes: {trace_nodes}"
print(f"[PASS] trace has >= 2 entries: {trace_nodes}")

print()
print("TEST 1 PASSED")

# ---------------------------------------------------------------------------
# Test 2: unary run_graph regression (offline)
# ---------------------------------------------------------------------------

print()
print("=" * 60)
print("TEST 2: run_graph unary regression (offline)")
print("=" * 60)

result = run_graph("write a hello world python script")
print(f"  route  = {result['route']!r}")
print(f"  status = {result['status']!r}")
print(f"  output_len = {len(result['output'])}")
print(f"  output_preview = {repr(result['output'][:80])}")
print(f"  trace_entries = {len(result['trace'])}")

assert result["status"] in ("OK", "FAILED"), f"Unexpected status: {result['status']}"
assert len(result["output"]) > 0, "unary output should be non-empty"
assert result["route"] in ("research", "coding", "review", "audit"), \
    f"Unexpected route: {result['route']}"
print()
print(f"[PASS] unary run_graph: route={result['route']!r} status={result['status']!r}")
print("TEST 2 PASSED")

print()
print("=" * 60)
print("ALL SMOKE TESTS PASSED")
print("=" * 60)
