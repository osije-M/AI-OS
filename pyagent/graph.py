"""
LangGraph supervisor -> worker graph for AgentRuntime.

State:
  task      - the user task string
  output    - final answer string
  trace     - list of NodeTrace dicts {node, type, summary, latency_ms}

Nodes:
  supervisor  - control node: decides routing (always -> worker in prototype)
  worker      - LLM node: calls DeepSeek or falls back to offline echo

Optional:
  If TOOL_SERVICE_ADDR is reachable, worker calls the 'reverse' tool once
  and records a tool NodeTrace.  Failure is silently skipped.
"""

import os
import sys
import time
import json
import logging
from typing import TypedDict, Any

import grpc

from langgraph.graph import StateGraph, END

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# State definition
# ---------------------------------------------------------------------------

class AgentState(TypedDict):
    task: str
    output: str
    trace: list[dict[str, Any]]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _now_ms() -> int:
    return int(time.monotonic_ns() // 1_000_000)


def _make_trace(node: str, type_: str, summary: str, latency_ms: int) -> dict:
    return {
        "node": node,
        "type": type_,
        "summary": summary,
        "latency_ms": latency_ms,
    }


# ---------------------------------------------------------------------------
# Tool helper (optional, silent-fail)
# ---------------------------------------------------------------------------

def _call_tool_reverse(task: str, trace_id: str) -> tuple[str | None, dict | None]:
    """
    Try to call ToolService reverse tool.
    Returns (result_str, node_trace_dict) or (None, None) on failure.
    """
    tool_addr = os.getenv("TOOL_SERVICE_ADDR", "127.0.0.1:9200")
    try:
        # Import here so missing stubs don't break offline mode
        gen_dir = os.path.join(os.path.dirname(__file__), "gen")
        if gen_dir not in sys.path:
            sys.path.insert(0, gen_dir)
        from tool.v1 import tool_pb2, tool_pb2_grpc  # noqa: PLC0415

        t0 = _now_ms()
        channel = grpc.insecure_channel(tool_addr)
        stub = tool_pb2_grpc.ToolServiceStub(channel)
        req = tool_pb2.InvokeRequest(
            name="reverse",
            input_json=json.dumps({"text": task}),
            trace_id=trace_id,
        )
        resp = stub.Invoke(req, timeout=2)
        latency = _now_ms() - t0
        channel.close()
        if resp.ok:
            out = json.loads(resp.output_json) if resp.output_json else {}
            result = out.get("result", resp.output_json)
            trace_entry = _make_trace(
                "tool:reverse", "tool",
                f"reverse tool ok -> {result!r}",
                latency,
            )
            return result, trace_entry
    except Exception as exc:
        logger.debug("[tool] ToolService not reachable (%s), skipping", exc)
    return None, None


# ---------------------------------------------------------------------------
# LLM helper
# ---------------------------------------------------------------------------

def _call_llm(task: str) -> tuple[str, int]:
    """Call DeepSeek via openai SDK.  Falls back to offline echo if no key."""
    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    if not api_key:
        return f"[offline] echo: {task}", 0

    base_url = os.getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
    model = os.getenv("DEEPSEEK_MODEL", "deepseek-chat")

    from openai import OpenAI  # noqa: PLC0415
    client = OpenAI(api_key=api_key, base_url=base_url)
    t0 = _now_ms()
    resp = client.chat.completions.create(
        model=model,
        messages=[{"role": "user", "content": task}],
        max_tokens=512,
    )
    latency = _now_ms() - t0
    answer = resp.choices[0].message.content or ""
    return answer, latency


# ---------------------------------------------------------------------------
# Graph nodes
# ---------------------------------------------------------------------------

def supervisor_node(state: AgentState) -> AgentState:
    t0 = _now_ms()
    # Prototype: always route to worker (no dynamic branching yet).
    summary = f"supervisor received task, routing to worker"
    latency = _now_ms() - t0
    new_trace = state["trace"] + [_make_trace("supervisor", "control", summary, latency)]
    return {**state, "trace": new_trace}


def worker_node(state: AgentState) -> AgentState:
    task = state["task"]
    trace = list(state["trace"])

    # --- Optional tool call ---
    tool_result, tool_trace = _call_tool_reverse(task, "")
    if tool_trace:
        trace.append(tool_trace)

    # --- LLM call ---
    t0 = _now_ms()
    try:
        output, llm_latency = _call_llm(task)
        if llm_latency == 0:
            # offline path: latency from outer timer
            llm_latency = _now_ms() - t0
    except Exception as exc:
        logger.error("[worker] LLM error: %s", exc)
        output = f"[error] {exc}"
        llm_latency = _now_ms() - t0

    if tool_result is not None:
        # Append tool result to output for visibility
        output = f"{output}\n[tool:reverse] {tool_result}"

    summary = f"worker produced output ({len(output)} chars)"
    trace.append(_make_trace("worker", "llm", summary, llm_latency))

    return {**state, "output": output, "trace": trace}


# ---------------------------------------------------------------------------
# Build graph
# ---------------------------------------------------------------------------

def build_graph() -> Any:
    """Build and compile the supervisor -> worker StateGraph."""
    builder: StateGraph = StateGraph(AgentState)
    builder.add_node("supervisor", supervisor_node)
    builder.add_node("worker", worker_node)

    builder.set_entry_point("supervisor")
    builder.add_edge("supervisor", "worker")
    builder.add_edge("worker", END)

    return builder.compile()


# Singleton compiled graph (lazy-initialised per process)
_GRAPH = None


def get_graph() -> Any:
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = build_graph()
    return _GRAPH


def run_graph(task: str) -> dict:
    """
    Run the graph for a task.
    Returns dict with keys: output, trace (list of dicts).
    """
    graph = get_graph()
    initial: AgentState = {"task": task, "output": "", "trace": []}
    result = graph.invoke(initial)
    return {"output": result["output"], "trace": result["trace"]}
