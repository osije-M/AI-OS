"""
Smoke test for M2 Python smart layer (graph.py).

Covers four verification cases:
  1. Offline routing   - three tasks route to coding/review/research deterministically
  2. Offline loop      - reflect always PASS offline, no extra cycles, status=OK
  3. Fault injection   - L1 retry + L3 switch visible in trace, final status=FAILED
  4. Server startup    - server starts and listens on :9100 (offline mode)

Cases 1-3 call run_graph() directly (no server needed).
Case 4 starts server in a thread and calls RunGraph via gRPC.

Run with:
    PYTHONUTF8=1 python pyagent/smoke_test.py
"""

import os
import sys
import time
import threading

# Ensure gen stubs and pyagent itself are importable
_PYAGENT_DIR = os.path.dirname(__file__)
_GEN_DIR = os.path.join(_PYAGENT_DIR, "gen")
if _PYAGENT_DIR not in sys.path:
    sys.path.insert(0, _PYAGENT_DIR)
if _GEN_DIR not in sys.path:
    sys.path.insert(0, _GEN_DIR)

# Load .env before importing graph (graph reads env at call time, but dotenv
# must be loaded so DEEPSEEK_API_KEY is present/absent correctly)
from dotenv import load_dotenv  # noqa: E402
_ROOT = os.path.join(_PYAGENT_DIR, "..")
load_dotenv(os.path.join(_ROOT, ".env"), override=False)

# =========================================================
# Helpers
# =========================================================

PASS_COUNT = 0
FAIL_COUNT = 0

def _ok(msg: str):
    global PASS_COUNT
    PASS_COUNT += 1
    print(f"  [PASS] {msg}")

def _fail(msg: str):
    global FAIL_COUNT
    FAIL_COUNT += 1
    print(f"  [FAIL] {msg}")

def _assert(cond: bool, msg: str):
    if cond:
        _ok(msg)
    else:
        _fail(msg)

def _find_trace_summary(trace: list, keyword: str) -> bool:
    return any(keyword.lower() in t.get("summary", "").lower() for t in trace)

def _print_trace(trace: list):
    for i, t in enumerate(trace):
        print(f"    [{i}] node={t['node']!r:20s} type={t['type']!r:10s} "
              f"latency={t['latency_ms']}ms  summary={t['summary']!r}")


# =========================================================
# Case 1: Offline routing
# =========================================================

def test_offline_routing():
    print("\n--- Case 1: Offline routing (no API key) ---")

    # Ensure no real API key
    orig = os.environ.pop("DEEPSEEK_API_KEY", None)
    try:
        # Force re-import with clean state (module may be cached)
        import importlib
        import graph as gm
        importlib.reload(gm)

        cases = [
            ("write a quicksort function in Python", "coding"),
            ("review this code for bugs and security issues", "review"),
            ("what is a reentrancy vulnerability in Solidity", "research"),
        ]

        for task, expected_route in cases:
            result = gm.run_graph(task)
            trace = result["trace"]
            output = result["output"]
            status = result["status"]

            print(f"\n  task={task!r}")
            _print_trace(trace)
            print(f"  output={output[:80]!r}  status={status}")

            # Check route trace
            routed_entries = [t for t in trace if "routed ->" in t.get("summary", "")]
            if routed_entries:
                actual_route = routed_entries[0]["summary"].split("routed ->")[-1].strip()
            else:
                actual_route = "unknown"

            _assert(
                actual_route == expected_route,
                f"task routed to {actual_route!r} (expected {expected_route!r})",
            )
            _assert(
                _find_trace_summary(trace, f"routed -> {expected_route}"),
                f"trace contains '[control] routed -> {expected_route}'",
            )
            _assert(status == "OK", f"status=OK (offline path always ok)")
            _assert("[offline]" in output, "output contains [offline] marker")
    finally:
        if orig is not None:
            os.environ["DEEPSEEK_API_KEY"] = orig


# =========================================================
# Case 2: Offline loop (reflect always PASS)
# =========================================================

def test_offline_loop():
    print("\n--- Case 2: Offline loop (reflect fixed PASS, no cycles) ---")

    orig = os.environ.pop("DEEPSEEK_API_KEY", None)
    try:
        import importlib
        import graph as gm
        importlib.reload(gm)

        result = gm.run_graph("explain what a reentrance vulnerability is")
        trace = result["trace"]
        status = result["status"]

        print(f"\n  status={status}")
        _print_trace(trace)

        reflect_entries = [t for t in trace if t["node"] == "reflect"]
        _assert(len(reflect_entries) == 1, f"exactly 1 reflect node (got {len(reflect_entries)})")
        _assert(
            all("PASS" in t["summary"] for t in reflect_entries),
            "all reflect entries are PASS",
        )
        _assert(status == "OK", "status=OK")

        # No RETRY should appear in trace
        has_retry = any("RETRY" in t.get("summary", "") for t in trace)
        _assert(not has_retry, "no RETRY in trace (offline fixed PASS)")

        loop_entries = [t for t in trace if t["node"] in ("research", "coding", "review")]
        _assert(len(loop_entries) == 1, f"worker called exactly once (got {len(loop_entries)})")
    finally:
        if orig is not None:
            os.environ["DEEPSEEK_API_KEY"] = orig


# =========================================================
# Case 3: Fault injection - L1 retry + L3 switch -> FAILED
# =========================================================

def test_fault_injection():
    print("\n--- Case 3: Fault injection (bad URL, L1 retry + L3 switch -> FAILED) ---")

    # Set dummy key and unreachable base URL
    os.environ["DEEPSEEK_API_KEY"] = "dummy-test-key-that-is-invalid"
    os.environ["DEEPSEEK_BASE_URL"] = "http://127.0.0.1:1"   # nothing listening here
    os.environ["LLM_MAX_RETRIES"] = "1"        # 1 retry (2 total attempts per model)
    os.environ["LLM_RETRY_BACKOFF_MS"] = "100" # fast for test
    os.environ["LLM_CALL_TIMEOUT_S"] = "3"     # short timeout

    try:
        import importlib
        import graph as gm
        importlib.reload(gm)

        print("  (running with bad base URL - expect connection errors...)")
        t_start = time.monotonic()
        result = gm.run_graph("write a hello world program")
        elapsed = time.monotonic() - t_start

        trace = result["trace"]
        output = result["output"]
        status = result["status"]

        print(f"\n  elapsed={elapsed:.1f}s  status={status}")
        print(f"  output={output[:120]!r}")
        _print_trace(trace)

        # L1 retry events
        retry_events = [t for t in trace if t["node"] == "llm_retry"]
        _assert(len(retry_events) >= 1, f"at least 1 L1 retry event (got {len(retry_events)})")

        # L3 switch event
        switch_events = [t for t in trace if t["node"] == "llm_switch"]
        _assert(len(switch_events) >= 1, f"at least 1 L3 switch event (got {len(switch_events)})")

        # Switch summary should name the fallback model
        if switch_events:
            switch_summary = switch_events[0]["summary"]
            _assert(
                "primary failed" in switch_summary.lower() or "switch" in switch_summary.lower(),
                f"switch trace says 'primary failed': {switch_summary!r}",
            )

        # Final status must be FAILED (not OK / not lying)
        _assert(status == "FAILED", f"status=FAILED (both models failed, got {status!r})")

        # Output should contain [error]
        _assert("[error]" in output, f"output contains [error]: {output[:80]!r}")

    finally:
        # Clean up injected env vars
        for k in ("DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "LLM_MAX_RETRIES",
                  "LLM_RETRY_BACKOFF_MS", "LLM_CALL_TIMEOUT_S"):
            os.environ.pop(k, None)


# =========================================================
# Case 4: Server startup (gRPC, offline mode)
# =========================================================

def test_server_startup():
    print("\n--- Case 4: Server startup (gRPC :9100, offline) ---")

    # Import server module FIRST (its module-level load_dotenv runs at import time).
    # Then clear the key so the graph runs in offline mode.
    import server as srv_mod  # noqa: F401 - triggers module-level load_dotenv

    # Force offline: clear key AFTER server module import so load_dotenv won't re-add it
    os.environ["DEEPSEEK_API_KEY"] = ""  # set to empty string rather than pop

    try:
        import importlib
        import graph as gm
        importlib.reload(gm)

        # Start server in a daemon thread
        server_thread = threading.Thread(target=srv_mod.serve, daemon=True)
        server_thread.start()
        time.sleep(1.5)  # give server time to bind

        import grpc
        from agent.v1 import agent_pb2, agent_pb2_grpc

        channel = grpc.insecure_channel("127.0.0.1:9100")
        stub = agent_pb2_grpc.AgentRuntimeStub(channel)
        req = agent_pb2.RunGraphRequest(
            trace_id="smoke-case4",
            task="what is a reentrance vulnerability",
            agent="supervisor",
        )

        print("  sending RunGraph via gRPC...")
        try:
            reply = stub.RunGraph(req, timeout=10)
        except grpc.RpcError as e:
            _fail(f"gRPC error: {e.code()} - {e.details()}")
            channel.close()
            return

        print(f"  status={reply.status}  output={reply.output[:80]!r}")
        print(f"  trace ({len(reply.trace)} entries):")
        for i, nt in enumerate(reply.trace):
            print(f"    [{i}] node={nt.node!r:20s} type={nt.type!r} "
                  f"latency={nt.latency_ms}ms  summary={nt.summary!r}")

        _assert(reply.status == "OK", f"gRPC reply status=OK (got {reply.status!r})")
        _assert(bool(reply.output), "gRPC reply has non-empty output")
        _assert(len(reply.trace) >= 2, f"trace has >=2 entries (got {len(reply.trace)})")
        _assert(
            any("routed ->" in nt.summary for nt in reply.trace),
            "trace contains routing decision",
        )
        channel.close()
    finally:
        # Restore original key (None means unset)
        os.environ.pop("DEEPSEEK_API_KEY", None)


# =========================================================
# Main
# =========================================================

if __name__ == "__main__":
    print("=" * 65)
    print("M2 Smoke Tests")
    print("=" * 65)

    test_offline_routing()
    test_offline_loop()
    test_fault_injection()
    test_server_startup()

    print("\n" + "=" * 65)
    print(f"Results: {PASS_COUNT} passed, {FAIL_COUNT} failed")
    print("=" * 65)
    if FAIL_COUNT > 0:
        sys.exit(1)
    print("ALL PASS")
