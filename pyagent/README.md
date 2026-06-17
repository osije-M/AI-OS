# pyagent — Python/LangGraph AgentRuntime

gRPC server that implements `aios.agent.v1.AgentRuntime.RunGraph`, called by the Go Orchestrator.

## Architecture

```
Orchestrator (Go) --gRPC--> AgentRuntime (Python/LangGraph, :9100)
                                   |
                          supervisor -> worker
                                           |
                              (optional) ToolService (Go, :9200)
                              (optional) DeepSeek LLM
```

## Setup

### 1. Create venv

```bash
D:/APP/python3.12/python.exe -m venv pyagent/.venv
```

### 2. Install dependencies

```bash
# Use PyPI official source (local machine may have Tsinghua mirror configured)
pyagent/.venv/Scripts/python.exe -m pip install \
  --index-url https://pypi.org/simple/ \
  -r pyagent/requirements.txt
```

### 3. Generate Python gRPC stubs

```bash
PYTHON=pyagent/.venv/Scripts/python.exe bash scripts/gen-py.sh
# Output: pyagent/gen/agent/v1/agent_pb2.py  agent_pb2_grpc.py
#         pyagent/gen/tool/v1/tool_pb2.py    tool_pb2_grpc.py
```

### 4. Configure environment

```bash
cp .env.example .env
# Edit .env: fill DEEPSEEK_API_KEY (leave blank for offline/fallback mode)
```

## Start the server

```bash
# From repo root
PYTHONUTF8=1 pyagent/.venv/Scripts/python.exe pyagent/server.py
```

Server listens on `0.0.0.0:9100` by default. Override with `AGENT_RUNTIME_ADDR`.

## Smoke test (offline mode, no API key needed)

```bash
# Terminal 1
PYTHONUTF8=1 pyagent/.venv/Scripts/python.exe pyagent/server.py

# Terminal 2
PYTHONUTF8=1 pyagent/.venv/Scripts/python.exe pyagent/smoke_test.py
```

Expected output (offline):
```
[smoke] connecting to 127.0.0.1:9100
[smoke] sending RunGraph: task='hello' trace_id=smoke-test-001

============================================================
trace_id : smoke-test-001
status   : OK
elapsed  : ~2000ms
output   : '[offline] echo: hello'

trace (2 nodes):
  [0] node='supervisor'  type='control'  latency=0ms
       summary='supervisor received task, routing to worker'
  [1] node='worker'  type='llm'  latency=0ms
       summary='worker produced output (21 chars)'
============================================================
[smoke] PASS
```

## Graph internals (`graph.py`)

| Node | type | What it does |
|------|------|--------------|
| `supervisor` | control | Decides routing; in prototype always -> worker |
| `worker` | llm | Calls DeepSeek (offline echo if no key); optionally calls ToolService `reverse` tool |
| `tool:reverse` | tool | Recorded in trace when ToolService is reachable |

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `DEEPSEEK_API_KEY` | (empty) | DeepSeek API key; empty = offline fallback |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | LLM base URL |
| `DEEPSEEK_MODEL` | `deepseek-chat` | Model name |
| `AGENT_RUNTIME_ADDR` | `0.0.0.0:9100` | gRPC listen address |
| `AGENT_RUNTIME_WORKERS` | `4` | Thread pool size |
| `TOOL_SERVICE_ADDR` | `127.0.0.1:9200` | ToolService address (optional) |

## Known issues / notes

- **grpc stub import path**: generated `*_pb2_grpc.py` uses `from agent.v1 import agent_pb2` (relative to gen root), so `pyagent/gen` must be in `sys.path`. Both `server.py` and `smoke_test.py` insert it automatically.
- **pip mirror**: if the local pip.conf points to Tsinghua mirror and you are abroad, install fails. Always pass `--index-url https://pypi.org/simple/`.
- **GBK console**: set `PYTHONUTF8=1` on Windows to avoid `UnicodeEncodeError` when log output contains non-ASCII.
- **Port conflict**: if 9100 is in use from a previous crashed run, `taskkill /F /IM python.exe` on Windows.
