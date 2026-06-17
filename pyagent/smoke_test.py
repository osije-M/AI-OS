"""
Smoke test for AgentRuntime gRPC server.

Connects to localhost:9100, sends a RunGraph request with task="hello",
prints the returned output and trace.

Run AFTER starting the server:
    python pyagent/server.py &
    python pyagent/smoke_test.py
"""

import os
import sys
import time

_GEN_DIR = os.path.join(os.path.dirname(__file__), "gen")
if _GEN_DIR not in sys.path:
    sys.path.insert(0, _GEN_DIR)

import grpc
from agent.v1 import agent_pb2, agent_pb2_grpc

TARGET = os.getenv("AGENT_RUNTIME_ADDR", "127.0.0.1:9100")
TASK = "hello"
TRACE_ID = "smoke-test-001"


def main():
    print(f"[smoke] connecting to {TARGET}")
    channel = grpc.insecure_channel(TARGET)
    stub = agent_pb2_grpc.AgentRuntimeStub(channel)

    req = agent_pb2.RunGraphRequest(
        trace_id=TRACE_ID,
        task=TASK,
        agent="supervisor",
    )

    print(f"[smoke] sending RunGraph: task={TASK!r} trace_id={TRACE_ID}")
    t0 = time.monotonic()
    try:
        reply = stub.RunGraph(req, timeout=30)
    except grpc.RpcError as e:
        print(f"[smoke] RPC ERROR: {e.code()} - {e.details()}")
        sys.exit(1)
    elapsed = int((time.monotonic() - t0) * 1000)

    print()
    print("=" * 60)
    print(f"trace_id : {reply.trace_id}")
    print(f"status   : {reply.status}")
    print(f"elapsed  : {elapsed}ms")
    print(f"output   : {reply.output!r}")
    print()
    print(f"trace ({len(reply.trace)} nodes):")
    for i, nt in enumerate(reply.trace):
        print(f"  [{i}] node={nt.node!r}  type={nt.type!r}  latency={nt.latency_ms}ms")
        print(f"       summary={nt.summary!r}")
    print("=" * 60)

    assert reply.status == "OK", f"Expected OK, got {reply.status!r}"
    assert reply.output, "output must not be empty"
    assert len(reply.trace) >= 2, "expect at least supervisor + worker traces"
    print("[smoke] PASS")
    channel.close()


if __name__ == "__main__":
    main()
