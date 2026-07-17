"""
AgentRuntime gRPC server.

Implements aios.agent.v1.AgentRuntime.RunGraph and listens on :9100
(configurable via AGENT_RUNTIME_ADDR env var).

Usage:
    python pyagent/server.py
or:
    PYTHONUTF8=1 python pyagent/server.py
"""

import os
import sys
import logging
import time
from concurrent import futures

# Make sure the generated stubs are importable as "agent.v1.agent_pb2" etc.
_GEN_DIR = os.path.join(os.path.dirname(__file__), "gen")
if _GEN_DIR not in sys.path:
    sys.path.insert(0, _GEN_DIR)

# Load .env before anything else (looks for .env in repo root, then cwd)
from dotenv import load_dotenv  # noqa: E402

_ROOT = os.path.join(os.path.dirname(__file__), "..")
load_dotenv(os.path.join(_ROOT, ".env"), override=False)

# logging 必须在 import graph 之前配置：graph.py 模块级代码(路由档案加载等)
# 会在 import 时打 INFO 日志，晚配置会让这些日志被无 handler 丢弃(M7-1 踩坑)。
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("agent_runtime")

import grpc  # noqa: E402
from agent.v1 import agent_pb2, agent_pb2_grpc  # noqa: E402
from graph import run_graph, run_graph_stream  # noqa: E402


# ---------------------------------------------------------------------------
# gRPC servicer
# ---------------------------------------------------------------------------

class AgentRuntimeServicer(agent_pb2_grpc.AgentRuntimeServicer):
    def RunGraphStream(self, request, context):
        """Server-streaming RPC: route + stream worker tokens. No reflect loop."""
        trace_id = request.trace_id
        task = request.task

        logger.info(
            "RunGraphStream trace_id=%s task=%r",
            trace_id, task[:80],
        )
        t_start = time.monotonic()

        try:
            for ev in run_graph_stream(task, trace_id, params=dict(request.params)):
                ev_type = ev.get("type", "")

                if ev_type == "node":
                    yield agent_pb2.StreamEvent(
                        type="node",
                        trace_id=trace_id,
                        node=ev.get("node", ""),
                        content=ev.get("content", ""),
                    )

                elif ev_type == "token":
                    yield agent_pb2.StreamEvent(
                        type="token",
                        trace_id=trace_id,
                        node=ev.get("node", ""),
                        content=ev.get("content", ""),
                    )

                elif ev_type == "done":
                    # Build the final RunGraphReply and attach it as StreamEvent.final
                    trace_protos = [
                        agent_pb2.NodeTrace(
                            node=t["node"],
                            type=t["type"],
                            summary=t["summary"],
                            latency_ms=t["latency_ms"],
                        )
                        for t in ev.get("trace", [])
                    ]
                    final_reply = agent_pb2.RunGraphReply(
                        trace_id=trace_id,
                        output=ev.get("output", ""),
                        status=ev.get("status", "FAILED"),
                        route=ev.get("route", ""),
                        trace=trace_protos,
                        prompt_tokens=ev.get("prompt_tokens", 0),
                        completion_tokens=ev.get("completion_tokens", 0),
                    )
                    elapsed_ms = int((time.monotonic() - t_start) * 1000)
                    logger.info(
                        "RunGraphStream done trace_id=%s status=%s elapsed=%dms output_len=%d",
                        trace_id, ev.get("status"), elapsed_ms, len(ev.get("output", "")),
                    )
                    yield agent_pb2.StreamEvent(
                        type="done",
                        trace_id=trace_id,
                        node=ev.get("route", ""),
                        content="",
                        final=final_reply,
                    )

                elif ev_type == "error":
                    logger.error("RunGraphStream error trace_id=%s: %s", trace_id, ev.get("content"))
                    yield agent_pb2.StreamEvent(
                        type="error",
                        trace_id=trace_id,
                        content=ev.get("content", "unknown error"),
                    )

        except Exception as exc:
            logger.exception("RunGraphStream unexpected error trace_id=%s: %s", trace_id, exc)
            yield agent_pb2.StreamEvent(
                type="error",
                trace_id=trace_id,
                content=f"server error: {exc}",
            )

    def RunGraph(self, request, context):
        trace_id = request.trace_id
        task = request.task
        agent_name = request.agent or "supervisor"

        logger.info(
            "RunGraph trace_id=%s agent=%s task=%r",
            trace_id, agent_name, task[:80],
        )
        t_start = time.monotonic()

        try:
            result = run_graph(task, params=dict(request.params))
        except Exception as exc:
            logger.exception("RunGraph failed: %s", exc)
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(exc))
            return agent_pb2.RunGraphReply(
                trace_id=trace_id,
                output="",
                status="FAILED",
            )

        elapsed_ms = int((time.monotonic() - t_start) * 1000)
        status = result.get("status", "FAILED")
        logger.info(
            "RunGraph done trace_id=%s status=%s elapsed=%dms output_len=%d",
            trace_id, status, elapsed_ms, len(result["output"]),
        )

        # Convert trace dicts to proto NodeTrace messages
        trace_protos = [
            agent_pb2.NodeTrace(
                node=t["node"],
                type=t["type"],
                summary=t["summary"],
                latency_ms=t["latency_ms"],
            )
            for t in result["trace"]
        ]

        return agent_pb2.RunGraphReply(
            trace_id=trace_id,
            output=result["output"],
            status=status,
            trace=trace_protos,
            route=result.get("route", ""),
            prompt_tokens=result.get("prompt_tokens", 0),
            completion_tokens=result.get("completion_tokens", 0),
        )


# ---------------------------------------------------------------------------
# Server bootstrap
# ---------------------------------------------------------------------------

def serve():
    addr = os.getenv("AGENT_RUNTIME_ADDR", "0.0.0.0:9100")
    max_workers = int(os.getenv("AGENT_RUNTIME_WORKERS", "4"))
    stop_grace_s = float(os.getenv("AGENT_RUNTIME_STOP_GRACE_S", "5"))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers))
    agent_pb2_grpc.add_AgentRuntimeServicer_to_server(AgentRuntimeServicer(), server)
    server.add_insecure_port(addr)

    # M7-0: graceful shutdown on SIGTERM/SIGINT — stop accepting new RPCs, give
    # in-flight requests up to stop_grace_s to finish (compose stop_grace_period
    # must be >= this, see docker-compose.yml). Windows native runs have weak
    # SIGTERM semantics; the container (Linux) is the primary target.
    import signal
    import threading
    stop_event = threading.Event()

    def _graceful_shutdown(signum, _frame):
        logger.info("received signal %s, graceful shutdown (grace=%.1fs)...", signum, stop_grace_s)
        server.stop(grace=stop_grace_s)
        stop_event.set()

    for sig in (signal.SIGTERM, signal.SIGINT):
        try:
            signal.signal(sig, _graceful_shutdown)
        except (ValueError, OSError):  # non-main thread / unsupported platform
            logger.warning("cannot register handler for signal %s", sig)

    server.start()
    logger.info("AgentRuntime gRPC server listening on %s", addr)
    server.wait_for_termination()
    logger.info("AgentRuntime gRPC server stopped gracefully")


if __name__ == "__main__":
    serve()
