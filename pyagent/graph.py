"""
LangGraph supervisor -> worker graph for AgentRuntime.

M2 upgrade: dynamic routing (slice1) + failure recovery (slice2) + reflect loop (slice3).
M4 upgrade: audit worker via Tool Mesh (ToolService.Invoke("audit", ...)).

State:
  task        - the user task string
  route       - routing decision: research / coding / review / audit
  output      - final answer string
  trace       - list of NodeTrace dicts {node, type, summary, latency_ms}
  llm_ok      - True if LLM succeeded at least once
  loop_count  - number of reflect->worker cycles so far

Nodes:
  supervisor    - control node: LLM intent classification (keyword fallback offline)
  research_node - worker: research / explain / summarize
  coding_node   - worker: write / modify code
  review_node   - worker: review / audit / bug-hunt
  audit_node    - worker: Solidity smart-contract security audit via Tool Mesh
  reflect_node  - control node: judge output quality, PASS or RETRY (bounded loop)

LLM helper (_call_llm):
  L1 retry  - retryable errors (timeout/rate-limit/5xx) up to LLM_MAX_RETRIES
  L3 switch - after primary exhausted, try DEEPSEEK_FALLBACK_MODEL
  offline   - no DEEPSEEK_API_KEY -> deterministic echo, no retries

Tool helper (_call_tool_reverse):
  Optional gRPC call to ToolService; silent-fail if unreachable.

Tool helper (_call_tool_audit):
  gRPC call to ToolService.Invoke("audit", {source, rule_only}); graceful-degrade on failure.
"""

import os
import sys
import time
import json
import random
import logging
from typing import TypedDict, Any

import grpc

from langgraph.graph import StateGraph, END

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Env config (read once at module load so tests can patch os.environ)
# ---------------------------------------------------------------------------

def _env_int(key: str, default: int) -> int:
    try:
        return int(os.getenv(key, str(default)))
    except (ValueError, TypeError):
        return default

def _env_float(key: str, default: float) -> float:
    try:
        return float(os.getenv(key, str(default)))
    except (ValueError, TypeError):
        return default

MAX_LOOPS: int = _env_int("MAX_LOOPS", 1)

# ---------------------------------------------------------------------------
# State definition
# ---------------------------------------------------------------------------

class AgentState(TypedDict):
    task: str
    route: str                # routing decision: research / coding / review / audit
    output: str
    trace: list[dict[str, Any]]
    llm_ok: bool              # True if at least one LLM call succeeded
    loop_count: int           # reflect -> worker cycles so far
    prompt_tokens: int        # cumulative prompt tokens across all LLM calls (router+worker+reflect)
    completion_tokens: int    # cumulative completion tokens across all LLM calls


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
# Audit tool helper (optional, graceful-degrade)
# ---------------------------------------------------------------------------

def _call_tool_audit(source: str, rule_only: bool = False) -> tuple[dict | None, dict | None]:
    """
    Call ToolService.Invoke("audit", {source, rule_only}) via gRPC.
    Returns (result_dict, node_trace_dict) or (None, trace_with_error) on failure.
    The caller should NEVER raise; always check result_dict for None to detect failure.
    """
    tool_addr = os.getenv("TOOL_SERVICE_ADDR", "127.0.0.1:9200")
    t0 = _now_ms()
    try:
        gen_dir = os.path.join(os.path.dirname(__file__), "gen")
        if gen_dir not in sys.path:
            sys.path.insert(0, gen_dir)
        from tool.v1 import tool_pb2, tool_pb2_grpc  # noqa: PLC0415

        channel = grpc.insecure_channel(tool_addr)
        stub = tool_pb2_grpc.ToolServiceStub(channel)
        req = tool_pb2.InvokeRequest(
            name="audit",
            input_json=json.dumps({"source": source, "rule_only": rule_only}),
            trace_id="",
        )
        resp = stub.Invoke(req, timeout=30)
        latency = _now_ms() - t0
        channel.close()

        if resp.ok:
            result = json.loads(resp.output_json) if resp.output_json else {}
            trace_entry = _make_trace(
                "tool:audit", "tool",
                f"audit ok: is_reentrancy={result.get('is_reentrancy')}, "
                f"confidence={result.get('confidence', 0):.2f}",
                latency,
            )
            return result, trace_entry
        else:
            err_msg = resp.error or "unknown error"
            trace_entry = _make_trace(
                "tool:audit", "tool",
                f"audit tool returned ok=false: {err_msg}",
                latency,
            )
            return None, trace_entry

    except Exception as exc:
        latency = _now_ms() - t0
        logger.debug("[tool:audit] ToolService not reachable (%s)", exc)
        trace_entry = _make_trace(
            "tool:audit", "tool",
            f"audit tool unreachable: {type(exc).__name__}: {exc}",
            latency,
        )
        return None, trace_entry


# ---------------------------------------------------------------------------
# LLM helper - structured return with L1 retry + L3 model-switch
# ---------------------------------------------------------------------------

def _call_llm(task: str, system_prompt: str = "", timeout_override: float | None = None,
              temperature: float | None = None, model_override: str | None = None,
              max_tokens: int | None = None) -> dict:
    """
    Call DeepSeek LLM with L1 retry and L3 model-switch.

    Args:
        task:             user/task message
        system_prompt:    optional system message
        timeout_override: override LLM_CALL_TIMEOUT_S for this call (e.g. fast routing)
        temperature:      sampling temperature; pass 0 for deterministic classification
                          (routing / reflect). None → provider default (workers).
        model_override:   M7-1 route profile model; None → DEEPSEEK_MODEL env.
                          L3 fallback model is unaffected (still DEEPSEEK_FALLBACK_MODEL).
        max_tokens:       M7-1 route profile output cap; None → default 512.

    Returns:
        {
          "output":     str,
          "latency_ms": int,
          "ok":         bool,
          "model_used": str,
          "attempts":   int,
          "events":     list[dict],  # NodeTrace entries for retry/switch events
          "prompt_tokens":     int,  # from resp.usage.prompt_tokens; 0 offline/on failure
          "completion_tokens": int,  # from resp.usage.completion_tokens; 0 offline/on failure
        }

    Offline (no DEEPSEEK_API_KEY): returns echo immediately, ok=True.
    """
    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    if not api_key:
        return {
            "output": f"[offline] echo: {task}",
            "latency_ms": 0,
            "ok": True,
            "model_used": "offline",
            "attempts": 0,
            "events": [],
            "prompt_tokens": 0,
            "completion_tokens": 0,
        }

    base_url = os.getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
    primary_model = model_override or os.getenv("DEEPSEEK_MODEL", "deepseek-chat")
    fallback_model = os.getenv("DEEPSEEK_FALLBACK_MODEL", "deepseek-reasoner")
    max_retries = _env_int("LLM_MAX_RETRIES", 2)
    backoff_base_ms = _env_int("LLM_RETRY_BACKOFF_MS", 500)
    timeout_s = timeout_override if timeout_override is not None else _env_float("LLM_CALL_TIMEOUT_S", 30.0)

    from openai import OpenAI  # noqa: PLC0415
    from openai import (  # noqa: PLC0415
        APITimeoutError,
        APIConnectionError,
        RateLimitError,
        InternalServerError,
    )

    RETRYABLE = (APITimeoutError, APIConnectionError, RateLimitError, InternalServerError)

    client = OpenAI(api_key=api_key, base_url=base_url)

    messages = []
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})
    messages.append({"role": "user", "content": task})

    events: list[dict] = []
    total_attempts = 0

    def _try_model(model: str) -> tuple[str, int, int, int, int]:
        """Try one model with L1 retry. Returns (output, latency_ms, attempts, prompt_tokens, completion_tokens)."""
        nonlocal total_attempts
        attempt_count = 0
        last_exc = None

        for attempt in range(1, max_retries + 2):  # first call + up to max_retries retries
            attempt_count += 1
            total_attempts += 1
            t0 = _now_ms()
            try:
                create_kwargs = {
                    "model": model,
                    "messages": messages,
                    "max_tokens": max_tokens if max_tokens is not None else 512,
                    "timeout": timeout_s,
                }
                if temperature is not None:
                    create_kwargs["temperature"] = temperature
                resp = client.chat.completions.create(**create_kwargs)
                latency = _now_ms() - t0
                answer = resp.choices[0].message.content or ""
                prompt_tokens = 0
                completion_tokens = 0
                if getattr(resp, "usage", None) is not None:
                    prompt_tokens = getattr(resp.usage, "prompt_tokens", 0) or 0
                    completion_tokens = getattr(resp.usage, "completion_tokens", 0) or 0
                return answer, latency, attempt_count, prompt_tokens, completion_tokens
            except RETRYABLE as exc:
                last_exc = exc
                if attempt <= max_retries:
                    # exponential backoff with jitter
                    sleep_ms = backoff_base_ms * (2 ** (attempt - 1))
                    jitter_ms = random.randint(0, backoff_base_ms // 2)
                    sleep_s = (sleep_ms + jitter_ms) / 1000.0
                    logger.warning(
                        "[llm] retryable error (attempt %d/%d) on %s: %s; backoff %.2fs",
                        attempt, max_retries + 1, model, exc, sleep_s,
                    )
                    events.append(_make_trace(
                        "llm_retry", "llm",
                        f"retry attempt {attempt} ({model}): {type(exc).__name__}",
                        _now_ms() - t0,
                    ))
                    time.sleep(sleep_s)
                else:
                    raise
            except Exception:
                # non-retryable (e.g. 401 AuthenticationError) - propagate immediately
                raise

        raise last_exc  # should not reach here

    # --- Try primary model ---
    t_start = _now_ms()
    try:
        output, latency, attempts, prompt_tokens, completion_tokens = _try_model(primary_model)
        return {
            "output": output,
            "latency_ms": latency,
            "ok": True,
            "model_used": primary_model,
            "attempts": attempts,
            "events": events,
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
        }
    except Exception as exc1:
        logger.warning("[llm] primary model '%s' failed after retries: %s", primary_model, exc1)
        events.append(_make_trace(
            "llm_switch", "llm",
            f"primary failed -> switch to {fallback_model}: {type(exc1).__name__}",
            _now_ms() - t_start,
        ))

    # --- L3: try fallback model ---
    try:
        output, latency, attempts, prompt_tokens, completion_tokens = _try_model(fallback_model)
        return {
            "output": output,
            "latency_ms": latency,
            "ok": True,
            "model_used": fallback_model,
            "attempts": total_attempts,
            "events": events,
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
        }
    except Exception as exc2:
        logger.error("[llm] fallback model '%s' also failed: %s", fallback_model, exc2)
        return {
            "output": f"[error] all models failed: {exc2}",
            "latency_ms": _now_ms() - t_start,
            "ok": False,
            "model_used": "none",
            "attempts": total_attempts,
            "events": events,
            "prompt_tokens": 0,
            "completion_tokens": 0,
        }


# ---------------------------------------------------------------------------
# Routing helpers
# ---------------------------------------------------------------------------

_ROUTE_LABELS = {"research", "coding", "review", "audit"}

_SYSTEM_PROMPT_ROUTER = (
    "You are a task router. Read the user task and reply with EXACTLY ONE lowercase label "
    "from: research, coding, review, audit. No punctuation, no explanation.\n"
    "\n"
    "Label definitions:\n"
    "- research: answer a question, explain a concept, or look up a fact. "
    "Example: 'What is the capital of France?', 'Who wrote Hamlet?'\n"
    "- coding: write or implement new code, functions, or algorithms. "
    "Example: 'Write a function to reverse a string', 'Implement bubble sort'\n"
    "- review: critique, inspect, or find bugs / vulnerabilities / smells in PROVIDED code or "
    "text, including general security review of ordinary code. "
    "Example: 'Review this code for bugs', 'Inspect this snippet for security vulnerabilities'\n"
    "- audit: ONLY for auditing Solidity / smart-contract code (reentrancy, on-chain security). "
    "Example: 'Audit this Solidity contract for reentrancy'\n"
    "\n"
    "Important: use 'audit' ONLY when the task is clearly about Solidity or smart contracts. "
    "General security or vulnerability review of ordinary code is 'review', NOT 'audit'. "
    "Plain factual questions are 'research'. When unsure, prefer research.\n"
    "Reply with only the label word."
)

# Audit keywords: Solidity-specific signals that unambiguously indicate smart-contract audit.
# These are high-precision; generic security terms (security, audit, vulnerab) are excluded
# to avoid colliding with general code-review tasks.
_AUDIT_KEYWORDS_STRONG = (
    "pragma solidity", "pragma", "contract ", "合约", "重入", "reentrancy",
)
# Secondary audit signals: only route to audit when combined with at least one strong signal,
# OR used standalone in a Chinese/audit-specific phrasing.
_AUDIT_KEYWORDS_SECONDARY = (
    "审计", "solidity",
)


def _is_audit_task(task_lower: str) -> bool:
    """Return True if the task should be routed to the audit worker.

    Rule: at least one strong Solidity-specific signal present,
    OR one of the secondary audit-specific terms that imply contract context.
    Generic security/vulnerability words alone do NOT trigger audit.
    """
    t = task_lower
    if any(k in t for k in _AUDIT_KEYWORDS_STRONG):
        return True
    if any(k in t for k in _AUDIT_KEYWORDS_SECONDARY):
        return True
    return False


def _keyword_route(task: str) -> str:
    """Deterministic keyword-based routing (offline fallback).

    Priority: audit > review > coding > research
    audit is checked first (highest priority) because Solidity/contract tasks are specific.
    review is checked before coding because review tasks often also contain 'code'.
    """
    t = task.lower()
    # Audit signals: Solidity/contract-specific keywords (highest priority)
    if _is_audit_task(t):
        return "audit"
    # Review signals take priority over coding (review tasks often say "review this code")
    if any(k in t for k in ("review", "bug", "audit", "inspect", "smell",
                             "審查", "审查", "审视", "漏洞", "vulnerab", "security", "安全")):
        return "review"
    # Coding signals
    if any(k in t for k in ("code", "codes", "coding", "implement", "write", "function",
                             "class", "algorithm", "script", "debug", "program",
                             "代码", "实现", "函数", "算法")):
        return "coding"
    return "research"


def _llm_route(task: str) -> tuple[str, int, int]:
    """Use LLM for intent classification; fall back to keyword on any failure.

    Audit keyword pre-check runs BEFORE the LLM call: if the task contains Solidity/
    contract signals, route immediately to audit without spending LLM tokens on routing.
    Uses a short timeout (LLM_ROUTE_TIMEOUT_S, default 8s) so routing failures
    degrade quickly to keyword fallback without blocking the main task LLM budget.

    Returns (route, prompt_tokens, completion_tokens) — the router call also costs money,
    so its usage is reported back to the caller for accumulation into the graph state.
    Fast-path/keyword-fallback outcomes report (route, 0, 0) since no LLM call was made.
    """
    # Fast path: audit-specific keywords detected offline before calling LLM
    t = task.lower()
    if _is_audit_task(t):
        return "audit", 0, 0

    route_timeout = _env_float("LLM_ROUTE_TIMEOUT_S", 8.0)
    try:
        res = _call_llm(task, system_prompt=_SYSTEM_PROMPT_ROUTER, timeout_override=route_timeout,
                        temperature=0)
    except Exception:
        return _keyword_route(task), 0, 0
    prompt_tokens = res.get("prompt_tokens", 0)
    completion_tokens = res.get("completion_tokens", 0)
    if not res["ok"]:
        return _keyword_route(task), prompt_tokens, completion_tokens
    label = res["output"].strip().lower().split()[0] if res["output"].strip() else ""
    if label in _ROUTE_LABELS:
        return label, prompt_tokens, completion_tokens
    # parse failed - use keyword fallback
    logger.debug("[router] LLM returned unexpected label %r, using keyword fallback", label)
    return _keyword_route(task), prompt_tokens, completion_tokens


# ---------------------------------------------------------------------------
# Worker helper
# ---------------------------------------------------------------------------

_SYSTEM_PROMPTS = {
    "research": (
        "You are a research assistant. Explain, summarize, and answer questions clearly. "
        "Provide factual, well-structured responses."
    ),
    "coding": (
        "You are a coding assistant. Write correct, clean, and well-commented code. "
        "Use the language that fits the task, default to Python when unspecified."
    ),
    "review": (
        "You are a code reviewer and security auditor. Identify bugs, security issues, "
        "and code smells. Be specific about line-level problems and suggest fixes."
    ),
}

# ---------------------------------------------------------------------------
# M7-1 路由能力档案：per-route 的 model/temperature/max_tokens（软约束）。
# configs/route-profiles.yaml，路径可用 ROUTE_PROFILES_FILE 覆盖；
# 缺失/损坏时安全降级为空档案（等价于此前行为），仅打警告日志。
# ---------------------------------------------------------------------------

def _load_route_profiles(path: str | None = None) -> dict:
    """Load per-route capability profiles. Returns {} on any failure (safe degrade)."""
    import yaml  # noqa: PLC0415

    rel = path or os.getenv("ROUTE_PROFILES_FILE", "configs/route-profiles.yaml")
    # 双重解析：先按 cwd（native 从 repo root 跑 / 容器 WORKDIR /app），
    # 再按 graph.py 位置回退（pytest 的 cwd 不可控）。
    candidates = [rel, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), rel)]
    for p in candidates:
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as f:
                data = yaml.safe_load(f) or {}
            routes = data.get("routes") or {}
            if not isinstance(routes, dict):
                raise ValueError("routes must be a mapping")
            logger.info("[profiles] route profiles loaded from %s: %s", p, sorted(routes.keys()))
            return routes
        except Exception as exc:
            logger.warning("[profiles] failed to load %s (%s), degrading to empty profiles", p, exc)
            return {}
    logger.warning("[profiles] route profiles file %r not found, degrading to empty profiles", rel)
    return {}


_ROUTE_PROFILES: dict = _load_route_profiles()


def _get_route_profile(route: str) -> dict:
    """Return the capability profile for a route ({} when absent)."""
    p = _ROUTE_PROFILES.get(route)
    return p if isinstance(p, dict) else {}


def _run_worker(state: AgentState, role: str) -> AgentState:
    """Shared worker implementation used by research/coding/review nodes."""
    task = state["task"]
    trace = list(state["trace"])

    # --- Optional tool call ---
    tool_result, tool_trace = _call_tool_reverse(task, "")
    if tool_trace:
        trace.append(tool_trace)

    # --- LLM call (M7-1: apply route capability profile) ---
    t0 = _now_ms()
    system_prompt = _SYSTEM_PROMPTS.get(role, "")
    profile = _get_route_profile(role)
    res = _call_llm(
        task,
        system_prompt=system_prompt,
        temperature=profile.get("temperature"),
        model_override=profile.get("model"),
        max_tokens=profile.get("max_tokens"),
    )

    # Extend trace with retry/switch events
    trace.extend(res["events"])

    output = res["output"]
    if tool_result is not None:
        output = f"{output}\n[tool:reverse] {tool_result}"

    latency = res["latency_ms"] if res["latency_ms"] > 0 else (_now_ms() - t0)
    summary = (
        f"{role} worker output via {res['model_used']} "
        f"({res['attempts']} attempt(s), {len(output)} chars)"
    )
    trace.append(_make_trace(role, "llm", summary, latency))

    return {
        **state,
        "output": output,
        "trace": trace,
        "llm_ok": res["ok"],
        "prompt_tokens": state.get("prompt_tokens", 0) + res.get("prompt_tokens", 0),
        "completion_tokens": state.get("completion_tokens", 0) + res.get("completion_tokens", 0),
    }


# ---------------------------------------------------------------------------
# Graph nodes
# ---------------------------------------------------------------------------

def supervisor_node(state: AgentState) -> AgentState:
    """Classify task intent and decide routing."""
    t0 = _now_ms()
    task = state["task"]

    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    if api_key:
        route, prompt_tokens, completion_tokens = _llm_route(task)
    else:
        route = _keyword_route(task)
        prompt_tokens, completion_tokens = 0, 0

    latency = _now_ms() - t0
    summary = f"[control] routed -> {route}"
    new_trace = state["trace"] + [_make_trace("supervisor", "control", summary, latency)]
    return {
        **state,
        "route": route,
        "trace": new_trace,
        "prompt_tokens": state.get("prompt_tokens", 0) + prompt_tokens,
        "completion_tokens": state.get("completion_tokens", 0) + completion_tokens,
    }


def research_node(state: AgentState) -> AgentState:
    return _run_worker(state, "research")


def coding_node(state: AgentState) -> AgentState:
    return _run_worker(state, "coding")


def review_node(state: AgentState) -> AgentState:
    return _run_worker(state, "review")


def audit_node(state: AgentState) -> AgentState:
    """
    Audit worker: extract Solidity source from task, call ToolService.Invoke("audit"),
    format result into output. Gracefully degrades when ToolService is unreachable.
    """
    task = state["task"]
    trace = list(state["trace"])
    t0 = _now_ms()

    # --- Extract Solidity source from task ---
    # Priority 1: code fence (```...```)
    source = None
    if "```" in task:
        import re
        # match ```solidity ... ``` or ``` ... ```
        m = re.search(r"```(?:solidity)?\s*\n?(.*?)```", task, re.DOTALL | re.IGNORECASE)
        if m:
            source = m.group(1).strip()
    # Priority 2: inline pragma/contract without fence
    if source is None:
        tl = task.lower()
        if "pragma" in tl or "contract " in tl:
            source = task.strip()
    # Fallback: send whole task as source (let auditor deal with it)
    if source is None:
        source = task.strip()

    # --- Call ToolService.Invoke("audit") ---
    result_dict, tool_trace = _call_tool_audit(source, rule_only=False)
    if tool_trace:
        trace.append(tool_trace)

    latency = _now_ms() - t0

    if result_dict is not None:
        # Format the audit result into human-readable output
        is_reentrancy = result_dict.get("is_reentrancy", False)
        confidence = result_dict.get("confidence", 0.0)
        locations = result_dict.get("locations", [])
        reason = result_dict.get("reason", "")
        fix = result_dict.get("fix", [])

        verdict = "[VULNERABLE]" if is_reentrancy else "[SAFE]"
        lines = [
            f"[audit] Reentrancy audit result: {verdict}",
            f"  Confidence : {confidence:.0%}",
        ]
        if locations:
            lines.append(f"  Locations  : {', '.join(str(loc) for loc in locations)}")
        if reason:
            lines.append(f"  Reason     : {reason}")
        if fix:
            lines.append("  Fix hints  :")
            for f_item in fix:
                lines.append(f"    - {f_item}")
        output = "\n".join(lines)
        llm_ok = True

        summary = (
            f"audit via tool:audit ok, is_reentrancy={is_reentrancy}, "
            f"confidence={confidence:.2f}, {len(locations)} location(s)"
        )
    else:
        # Graceful degradation: extract error from last tool:audit trace entry
        err_detail = ""
        for entry in reversed(trace):
            if entry.get("node") == "tool:audit":
                err_detail = entry.get("summary", "")
                break
        output = f"[audit] Service unavailable or returned error. {err_detail}"
        llm_ok = False
        summary = f"audit degraded: tool:audit not ok"

    trace.append(_make_trace("audit", "tool", summary, latency))

    return {
        **state,
        "output": output,
        "trace": trace,
        "llm_ok": llm_ok,
    }


def reflect_node(state: AgentState) -> AgentState:
    """Judge whether the output sufficiently answers the task."""
    t0 = _now_ms()
    task = state["task"]
    output = state["output"]
    loop_count = state.get("loop_count", 0)
    trace = list(state["trace"])

    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    prompt_tokens = 0
    completion_tokens = 0

    if not api_key:
        # offline: always PASS, never loop
        verdict = "PASS"
        summary = "[control] reflect -> PASS (offline, fixed)"
    elif loop_count >= MAX_LOOPS:
        # force PASS to prevent infinite loop
        verdict = "PASS"
        summary = f"[control] reflect -> PASS (max_loops={MAX_LOOPS} reached)"
    else:
        reflect_system = (
            "You are a lenient quality gate. The answer only needs to be reasonable and "
            "on-topic; it does NOT need to be perfect or exhaustive. Reply PASS unless the "
            "answer is empty, off-topic, or clearly wrong/incomplete. When in doubt, PASS. "
            "Reply with exactly one word: PASS or RETRY. "
            "If RETRY, add a colon and a one-sentence reason (no newlines)."
        )
        reflect_task = f"TASK: {task}\n\nANSWER: {output}"
        reflect_timeout = _env_float("LLM_REFLECT_TIMEOUT_S", 10.0)
        res = _call_llm(reflect_task, system_prompt=reflect_system, timeout_override=reflect_timeout,
                        temperature=0)
        prompt_tokens = res.get("prompt_tokens", 0)
        completion_tokens = res.get("completion_tokens", 0)

        if not res["ok"]:
            # LLM failed during reflect: conservatively PASS (don't add loop overhead)
            verdict = "PASS"
            summary = "[control] reflect -> PASS (reflect LLM failed, conservative)"
        else:
            raw = (res["output"] or "").strip().upper()
            if raw.startswith("RETRY"):
                verdict = "RETRY"
                reason = res["output"].strip()
                summary = f"[control] reflect -> RETRY: {reason}"
            else:
                verdict = "PASS"
                summary = f"[control] reflect -> PASS"

    latency = _now_ms() - t0
    trace.append(_make_trace("reflect", "control", summary, latency))
    new_loop = loop_count + (1 if verdict == "RETRY" else 0)

    return {
        **state,
        "trace": trace,
        "loop_count": new_loop,
        "route": state.get("route", "research"),
        "prompt_tokens": state.get("prompt_tokens", 0) + prompt_tokens,
        "completion_tokens": state.get("completion_tokens", 0) + completion_tokens,
    }


# ---------------------------------------------------------------------------
# Conditional edge functions
# ---------------------------------------------------------------------------

def route_fn(state: AgentState) -> str:
    """Route after supervisor: read state["route"]."""
    return state.get("route", "research")


def reflect_route(state: AgentState) -> str:
    """After reflect: check latest trace entry for verdict."""
    # Find the last reflect trace entry
    for entry in reversed(state["trace"]):
        if entry["node"] == "reflect":
            summary = entry["summary"]
            if "-> RETRY" in summary:
                return state.get("route", "research")  # back to the routed worker
            return "pass"
    return "pass"


# ---------------------------------------------------------------------------
# Build graph
# ---------------------------------------------------------------------------

def build_graph() -> Any:
    """Build and compile the multi-agent supervisor -> worker -> reflect StateGraph."""
    builder: StateGraph = StateGraph(AgentState)

    # Nodes
    builder.add_node("supervisor", supervisor_node)
    builder.add_node("research", research_node)
    builder.add_node("coding", coding_node)
    builder.add_node("review", review_node)
    builder.add_node("audit", audit_node)
    builder.add_node("reflect", reflect_node)

    # Entry
    builder.set_entry_point("supervisor")

    # Supervisor -> worker (conditional on route)
    builder.add_conditional_edges(
        "supervisor",
        route_fn,
        {"research": "research", "coding": "coding", "review": "review", "audit": "audit"},
    )

    # Workers -> reflect
    builder.add_edge("research", "reflect")
    builder.add_edge("coding", "reflect")
    builder.add_edge("review", "reflect")
    builder.add_edge("audit", "reflect")

    # Reflect -> END or back to worker
    builder.add_conditional_edges(
        "reflect",
        reflect_route,
        {
            "pass": END,
            "research": "research",
            "coding": "coding",
            "review": "review",
            "audit": "audit",
        },
    )

    return builder.compile()


# Singleton compiled graph (lazy-initialised per process)
_GRAPH = None


def get_graph() -> Any:
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = build_graph()
    return _GRAPH


def _demo_delay_seconds(params: dict | None) -> float:
    """M7-2 测试钩子:params.demo_delay_ms → 秒。

    仅离线模式(无 DEEPSEEK_API_KEY)生效——它的唯一用途是让 CI/演示能在
    /v1/runs 里抓到 RUNNING 状态;在线模式忽略并打警告(不污染真实请求语义)。
    上限 30s 防手滑。非法值按 0 处理。
    """
    if not params:
        return 0.0
    raw = params.get("demo_delay_ms", "")
    if not raw:
        return 0.0
    try:
        ms = int(raw)
    except (TypeError, ValueError):
        logger.warning("[demo] invalid demo_delay_ms %r ignored", raw)
        return 0.0
    if ms <= 0:
        return 0.0
    if os.getenv("DEEPSEEK_API_KEY", "").strip():
        logger.warning("[demo] demo_delay_ms ignored in online mode")
        return 0.0
    return min(ms, 30_000) / 1000.0


def run_graph(task: str, params: dict | None = None) -> dict:
    """
    Run the graph for a task.
    Returns dict with keys: output, trace (list of dicts), status ("OK" or "FAILED"),
    route, prompt_tokens, completion_tokens (cumulative usage across all LLM calls
    in the graph run: router + worker(s) + reflect; 0/0 in offline mode).
    params: 透传的请求参数(M7-2 仅识别测试钩子 demo_delay_ms,离线模式生效)。
    """
    delay = _demo_delay_seconds(params)
    if delay > 0:
        logger.info("[demo] offline demo delay %.1fs (trace visibility hook)", delay)
        time.sleep(delay)

    graph = get_graph()
    initial: AgentState = {
        "task": task,
        "route": "",
        "output": "",
        "trace": [],
        "llm_ok": False,
        "loop_count": 0,
        "prompt_tokens": 0,
        "completion_tokens": 0,
    }
    result = graph.invoke(initial)
    status = "OK" if result.get("llm_ok") else "FAILED"
    return {
        "output": result["output"],
        "trace": result["trace"],
        "status": status,
        "route": result.get("route", ""),
        "prompt_tokens": result.get("prompt_tokens", 0),
        "completion_tokens": result.get("completion_tokens", 0),
    }


# ---------------------------------------------------------------------------
# Streaming path: run_graph_stream
# ---------------------------------------------------------------------------

def run_graph_stream(task: str, trace_id: str = "", params: dict | None = None):
    """
    Generator: streaming version of run_graph.

    Flow: route -> yield supervisor node event -> stream worker tokens -> yield done.
    Does NOT run the reflect loop (streaming mode is route + stream-answer only).
    Existing unary run_graph is completely untouched.

    Yields plain dicts; caller (server.py) converts to proto StreamEvent.
      {"type":"node",  "node":"supervisor", "content":"routed -> coding"}
      {"type":"token", "node":"coding",     "content":"<delta>"}
      {"type":"done",  "output":..., "route":..., "status":..., "trace":[...]}
      {"type":"error", "content":"<err>"}
    """
    try:
        yield from _run_graph_stream_inner(task, trace_id, params)
    except Exception as exc:
        logger.exception("[stream] unexpected generator error: %s", exc)
        yield {"type": "error", "content": f"internal generator error: {exc}"}


def _run_graph_stream_inner(task: str, trace_id: str, params: dict | None = None):
    """Inner generator - wrapped by run_graph_stream for top-level exception safety."""
    trace: list[dict] = []

    # --- 1. Route (same logic as supervisor_node) ---
    t0 = _now_ms()
    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    router_prompt_tokens = 0
    router_completion_tokens = 0
    try:
        if api_key:
            route, router_prompt_tokens, router_completion_tokens = _llm_route(task)
        else:
            route = _keyword_route(task)
    except Exception as exc:
        logger.warning("[stream] routing failed, using keyword fallback: %s", exc)
        route = _keyword_route(task)

    supervisor_latency = _now_ms() - t0
    sup_trace = _make_trace("supervisor", "control", f"[control] routed -> {route}", supervisor_latency)
    trace.append(sup_trace)

    yield {
        "type": "node",
        "node": "supervisor",
        "content": f"routed -> {route}",
    }

    # --- 2. Worker: stream tokens or degrade ---
    full_output = ""
    llm_ok = False
    worker_latency_ms = 0
    worker_prompt_tokens = 0
    worker_completion_tokens = 0

    if route == "audit":
        # Audit is a one-shot tool call; degrade to single-chunk in stream mode
        try:
            t1 = _now_ms()
            # Extract source same as audit_node
            source = _extract_solidity_source(task)
            result_dict, tool_trace = _call_tool_audit(source, rule_only=False)
            worker_latency_ms = _now_ms() - t1
            if tool_trace:
                trace.append(tool_trace)

            if result_dict is not None:
                full_output = _format_audit_result(result_dict)
                llm_ok = True
            else:
                err_detail = ""
                for entry in reversed(trace):
                    if entry.get("node") == "tool:audit":
                        err_detail = entry.get("summary", "")
                        break
                full_output = f"[audit] Service unavailable or returned error. {err_detail}"
                llm_ok = False

            # Yield the whole audit result as a single token event
            yield {"type": "token", "node": "audit", "content": full_output}

            audit_summary = (
                f"audit (stream) via tool:audit, "
                f"is_reentrancy={result_dict.get('is_reentrancy') if result_dict else 'N/A'}"
            )
            trace.append(_make_trace("audit", "tool", audit_summary, worker_latency_ms))

        except Exception as exc:
            logger.exception("[stream] audit worker error: %s", exc)
            yield {"type": "error", "content": f"audit worker error: {exc}"}
            return

    else:
        # research / coding / review: DeepSeek streaming
        system_prompt = _SYSTEM_PROMPTS.get(route, "")
        t1 = _now_ms()
        try:
            if not api_key:
                # Offline mode: yield task echo in small chunks to simulate streaming
                delay = _demo_delay_seconds(params)
                if delay > 0:
                    logger.info("[demo] offline stream demo delay %.1fs", delay)
                    time.sleep(delay)
                offline_text = f"[offline] echo: {task}"
                chunk_size = 4
                for i in range(0, len(offline_text), chunk_size):
                    chunk = offline_text[i:i + chunk_size]
                    full_output += chunk
                    yield {"type": "token", "node": route, "content": chunk}
                llm_ok = True
            else:
                # Online: DeepSeek streaming with L3 fallback on pre-first-token error
                # Use a collector list to get return values from the sub-generator
                collector: list[dict] = []  # will hold {"output":..., "ok":..., "prompt_tokens":..., "completion_tokens":...}
                for ev in _stream_llm(task, system_prompt, route, collector,
                                      profile=_get_route_profile(route)):
                    yield ev
                if collector:
                    full_output = collector[0].get("output", "")
                    llm_ok = collector[0].get("ok", False)
                    worker_prompt_tokens = collector[0].get("prompt_tokens", 0)
                    worker_completion_tokens = collector[0].get("completion_tokens", 0)
                else:
                    llm_ok = False

            worker_latency_ms = _now_ms() - t1

        except Exception as exc:
            worker_latency_ms = _now_ms() - t1
            logger.exception("[stream] worker error for route=%s: %s", route, exc)
            yield {"type": "error", "content": f"{route} worker error: {exc}"}
            return

        trace.append(_make_trace(
            route, "llm",
            f"{route} worker stream ok, {len(full_output)} chars",
            worker_latency_ms,
        ))

    # --- 3. Done ---
    status = "OK" if llm_ok else "FAILED"
    yield {
        "type": "done",
        "output": full_output,
        "route": route,
        "status": status,
        "trace": trace,
        "prompt_tokens": router_prompt_tokens + worker_prompt_tokens,
        "completion_tokens": router_completion_tokens + worker_completion_tokens,
    }


def _stream_llm(task: str, system_prompt: str, node_name: str, collector: list,
                profile: dict | None = None):
    """
    Inner generator: streams tokens from DeepSeek with L3 fallback.

    Yields {"type":"token", "node":node_name, "content":delta} events.
    Appends {"output": str, "ok": bool, "prompt_tokens": int, "completion_tokens": int}
    to collector when done so caller can read the final result (generators can't
    easily return values to for-loop callers).

    Usage: requests stream_options={"include_usage": True} (DeepSeek is OpenAI-compatible;
    the final chunk then carries a non-empty `usage`). If the backend/SDK rejects that
    param, the create() call is retried without it and usage falls back to
    prompt_tokens=0, completion_tokens=<number of token events> (best-effort estimate).

    L3 fallback: if the primary model raises BEFORE the first token is seen,
    switch to DEEPSEEK_FALLBACK_MODEL and retry. If failure happens after the
    first token was already yielded, yield an error event and stop.
    """
    profile = profile or {}
    base_url = os.getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
    primary_model = profile.get("model") or os.getenv("DEEPSEEK_MODEL", "deepseek-chat")
    fallback_model = os.getenv("DEEPSEEK_FALLBACK_MODEL", "deepseek-reasoner")
    timeout_s = _env_float("LLM_CALL_TIMEOUT_S", 30.0)
    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()

    # M7-1 route profile → create() 共享参数（temperature/max_tokens 可选）
    profile_kwargs: dict = {}
    if profile.get("temperature") is not None:
        profile_kwargs["temperature"] = profile["temperature"]
    if profile.get("max_tokens") is not None:
        profile_kwargs["max_tokens"] = profile["max_tokens"]

    from openai import OpenAI  # noqa: PLC0415

    client = OpenAI(api_key=api_key, base_url=base_url)
    messages = []
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})
    messages.append({"role": "user", "content": task})

    full_output = ""
    first_token_seen = False
    token_event_count = 0
    prompt_tokens = 0
    completion_tokens = 0
    usage_captured = False

    def _do_stream(model: str):
        """Actually call the API and yield token dicts; mutates the usage/output nonlocals."""
        nonlocal full_output, first_token_seen, token_event_count
        nonlocal prompt_tokens, completion_tokens, usage_captured
        try:
            stream = client.chat.completions.create(
                model=model,
                messages=messages,
                stream=True,
                timeout=timeout_s,
                stream_options={"include_usage": True},
                **profile_kwargs,
            )
        except Exception as exc:
            logger.debug(
                "[stream] stream_options include_usage rejected (%s), retrying without it "
                "(usage will fall back to token-count)", exc,
            )
            stream = client.chat.completions.create(
                model=model,
                messages=messages,
                stream=True,
                timeout=timeout_s,
                **profile_kwargs,
            )
        for chunk in stream:
            delta = chunk.choices[0].delta.content if chunk.choices else None
            if delta:
                first_token_seen = True
                full_output += delta
                token_event_count += 1
                yield {"type": "token", "node": node_name, "content": delta}
            usage = getattr(chunk, "usage", None)
            if usage is not None:
                prompt_tokens = getattr(usage, "prompt_tokens", 0) or 0
                completion_tokens = getattr(usage, "completion_tokens", 0) or 0
                usage_captured = True

    def _finalize_usage():
        """Fall back to token-event-count usage if a real usage chunk was never seen."""
        nonlocal completion_tokens
        if not usage_captured:
            completion_tokens = token_event_count

    # Try primary model
    try:
        yield from _do_stream(primary_model)
        _finalize_usage()
        collector.append({
            "output": full_output, "ok": True,
            "prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens,
        })
        return
    except Exception as exc1:
        if first_token_seen:
            # Mid-stream failure: can't cleanly retry
            logger.error("[stream] primary model failed mid-stream: %s", exc1)
            _finalize_usage()
            collector.append({
                "output": full_output, "ok": False,
                "prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens,
            })
            return
        else:
            # Pre-first-token: L3 switch to fallback
            logger.warning(
                "[stream] primary model failed before first token, switching to %s: %s",
                fallback_model, exc1,
            )

    # L3: try fallback model
    try:
        yield from _do_stream(fallback_model)
        _finalize_usage()
        collector.append({
            "output": full_output, "ok": True,
            "prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens,
        })
    except Exception as exc2:
        logger.error("[stream] fallback model also failed: %s", exc2)
        err_text = f"[error] all models failed: {exc2}"
        full_output = full_output + err_text if full_output else err_text
        _finalize_usage()
        collector.append({
            "output": full_output, "ok": False,
            "prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens,
        })


def _extract_solidity_source(task: str) -> str:
    """Extract Solidity source from task string (same logic as audit_node)."""
    import re
    if "```" in task:
        m = re.search(r"```(?:solidity)?\s*\n?(.*?)```", task, re.DOTALL | re.IGNORECASE)
        if m:
            return m.group(1).strip()
    tl = task.lower()
    if "pragma" in tl or "contract " in tl:
        return task.strip()
    return task.strip()


def _format_audit_result(result_dict: dict) -> str:
    """Format audit result dict into human-readable string (same logic as audit_node)."""
    is_reentrancy = result_dict.get("is_reentrancy", False)
    confidence = result_dict.get("confidence", 0.0)
    locations = result_dict.get("locations", [])
    reason = result_dict.get("reason", "")
    fix = result_dict.get("fix", [])

    verdict = "[VULNERABLE]" if is_reentrancy else "[SAFE]"
    lines = [
        f"[audit] Reentrancy audit result: {verdict}",
        f"  Confidence : {confidence:.0%}",
    ]
    if locations:
        lines.append(f"  Locations  : {', '.join(str(loc) for loc in locations)}")
    if reason:
        lines.append(f"  Reason     : {reason}")
    if fix:
        lines.append("  Fix hints  :")
        for f_item in fix:
            lines.append(f"    - {f_item}")
    return "\n".join(lines)
