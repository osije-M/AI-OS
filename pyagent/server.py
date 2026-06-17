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

import grpc  # noqa: E402
from agent.v1 import agent_pb2, agent_pb2_grpc  # noqa: E402
from graph import run_graph  # noqa: E402

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("agent_runtime")


# ---------------------------------------------------------------------------
# gRPC servicer
# ---------------------------------------------------------------------------

class AgentRuntimeServicer(agent_pb2_grpc.AgentRuntimeServicer):
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
            result = run_graph(task)
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
        logger.info(
            "RunGraph done trace_id=%s status=OK elapsed=%dms output_len=%d",
            trace_id, elapsed_ms, len(result["output"]),
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
            status="OK",
            trace=trace_protos,
        )


# ---------------------------------------------------------------------------
# Server bootstrap
# ---------------------------------------------------------------------------

def serve():
    addr = os.getenv("AGENT_RUNTIME_ADDR", "0.0.0.0:9100")
    max_workers = int(os.getenv("AGENT_RUNTIME_WORKERS", "4"))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers))
    agent_pb2_grpc.add_AgentRuntimeServicer_to_server(AgentRuntimeServicer(), server)
    server.add_insecure_port(addr)
    server.start()
    logger.info("AgentRuntime gRPC server listening on %s", addr)
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
