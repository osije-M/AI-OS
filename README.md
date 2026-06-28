# AI-OS — LangGraph × Kratos 的 Agent 编排平台

> **一句话**：用 **Go/Kratos 写确定性控制面**（4 个微服务）+ **Python/LangGraph 写非确定性智能层**，把 LLM 能力工程化为可控、可扩展、可观测的分布式 Agent 编排系统。

对应架构愿景见 [`AI-Agent-Framework-AIOS-Architecture-v4.md`](AI-Agent-Framework-AIOS-Architecture-v4.md)（企业级蓝图）。本仓库是其**最小可运行纵向切片**的原型实现，按里程碑由薄到厚（M0→M3 已完成）。

---

## 核心理念：双流模型

v4 第 1 节的 `Deterministic Execution Kernel + Non-deterministic Intelligence Layer` 直接映射到技术栈：

| 流 | 职责 | 实现 |
|---|---|---|
| **Control Flow**（确定性、可 replay） | 路由 / 调度 / 重试 / 恢复 / 鉴权 / 防火墙 | **Go + Kratos** 微服务 |
| **Data Flow**（概率性、可变化） | LLM 推理 / 工具调用 / 自我反思 | **Python + LangGraph** |

> Control 必须稳定，Data 可以变化。用工程纪律驯服大模型的不确定性。

---

## 架构与服务拓扑（5 个进程）

```
Client
  │ HTTP  POST /v1/run   （由 gateway.proto 的 google.api.http 注解生成）
  ▼
┌──────────────┐
│  Gateway     │  :8000  入口/路由（Go/Kratos）
└──────┬───────┘
       │ gRPC
       ▼
┌──────────────┐   [Policy 防火墙] 执行前 Allow / Deny / Transform
│ Orchestrator │  :9300  编排控制面 + 重试（Go/Kratos）
└──────┬───────┘
       │ gRPC
       ▼
┌──────────────────────────────┐        ┌──────────────┐
│ AgentRuntime (Python/LangGraph)│ gRPC │ ToolService  │ :9200 工具即服务
│  supervisor →(动态路由)→        │─────▶│ (Go/Kratos)  │
│  research / coding / review     │        └──────────────┘
│  → reflect（有界循环）          │  :9100
└──────────────┬─────────────────┘
               │ 每次执行的链路 trace（异步、最佳努力）
               ▼
        ┌──────────────┐   GET /v1/trace/{id}
        │ TraceStore   │  :9400  可观测落盘（Go/Kratos, JSONL）
        └──────────────┘   → /viewer 网页 / tracectl CLI 渲染
```

- **Gateway**（Go）：对外 HTTP 入口，转发到 Orchestrator。`/v1/run` 由 proto 注解生成；`/v1/trace`、`/v1/traces`、`/viewer` 手写路由。
- **Orchestrator**（Go）：编排控制面。执行前过 **Policy 防火墙**；调 Python 智能层跑图，对瞬时错误**重试**；执行完把链路 trace 异步写入 TraceStore。
- **AgentRuntime**（Python/LangGraph）：多 Agent 图。supervisor 动态路由 → 专职 worker → reflect 自检（有界循环）；含 **L1 retry + L3 换模型**容错；无 key 走离线兜底。
- **ToolService**（Go）：Tool Mesh，"工具即服务"，暴露工具发现与调用。
- **TraceStore**（Go）：可观测性，按 trace_id 持久化执行链路，重启可回放。

---

## 关键概念（读懂这几个就懂了这个项目）

- **契约先行（contract-first）**：先用 `.proto` 文件把"有哪些服务、每个方法收什么发什么"定义成**语言无关的契约**（在 [`api/proto/`](api/proto/)），再由工具**自动生成各语言代码**：`buf` 生成 Go、`grpcio-tools` 生成 Python。于是 Go 和 Python "说同一种话"，谁都改不动接口而不被对方发现。改接口 = 改 proto → 重新生成 → 两端同步。HTTP 路由也由 proto 的 `google.api.http` 注解生成，连 URL 都是契约的一部分。
- **双流模型**：见上。确定性的事交给 Go，不确定的事交给 Python。
- **工具即服务（Tool Mesh）**：工具不是进程内函数，而是独立的 gRPC 服务，可独立部署、被多 Agent 共享。
- **可观测 / trace**：每次请求带一个贯穿全链路的 `trace_id`，每个节点（policy / 路由 / 工具 / LLM / 重试 / 反思）都留一条轨迹并持久化，事后可凭 id 完整还原"这次请求怎么走的"。
- **执行防火墙（Policy）**：请求进入智能层**之前**先过一道 Allow/Deny/Transform，命中高危规则直接拒绝、不浪费 LLM 调用。

---

## 目录结构

```
.
├── api/
│   ├── proto/            # 唯一契约源（5 份 proto，Go/Python 共享）
│   │   ├── gateway/v1/   # 对外入口（含 google.api.http 注解）
│   │   ├── orchestrator/v1/
│   │   ├── agent/v1/     # AgentRuntime：Python 实现、Go 调用
│   │   ├── tool/v1/      # ToolService
│   │   └── trace/v1/     # TraceStore（可观测）
│   └── gen/go/           # buf 生成的 Go 代码
├── app/                  # 4 个 Go/Kratos 微服务
│   ├── gateway/  orchestrator/  toolservice/  tracestore/
│   └── （orchestrator/internal/policy 为执行防火墙）
├── pyagent/              # Python/LangGraph 智能层（gRPC server + 编排图）
├── cmd/tracectl/         # 链路查看 CLI（lipgloss）
├── tools/trace-viewer/   # 链路查看网页（单文件，由 gateway /viewer 托管）
├── configs/policy.yaml   # Policy 规则
├── third_party/google/api/ # vendor 的第三方 proto（HTTP 注解用，离线）
├── scripts/              # gen.sh(Go) / gen-py.sh(Python) 代码生成
├── buf.yaml buf.gen.yaml # proto 工具链配置
└── AI-...-v4.md          # 架构愿景
```

---

## 工具链（本机已验证）

Go 1.22（+自动 go1.24 工具链）· **Kratos v2.9.2** · **buf 1.47.2**（替代 protoc，纯 Go）· protoc-gen-go/grpc/http · wire · Python 3.12 + LangGraph

> 本机网络坑与规避见个人知识库：goproxy.cn 挂 → 用 goproxy.io；protoc 二进制装不了 → 用 buf；第三方 google/api proto → 从 kratos 模块缓存 vendor。

## 代码生成

```bash
bash scripts/gen.sh      # 生成 Go（buf generate api/proto，third_party 仅供 import）
bash scripts/gen-py.sh   # 生成 Python stubs（grpcio-tools）
```

## 快速跑通（端到端已验证）

```bash
# 1. 4 个 Go 服务
go build -o bin/toolservice.exe  ./app/toolservice/cmd/toolservice
go build -o bin/orchestrator.exe ./app/orchestrator/cmd/orchestrator
go build -o bin/gateway.exe      ./app/gateway/cmd/gateway
go build -o bin/tracestore.exe   ./app/tracestore/cmd/tracestore
./bin/toolservice.exe &   # :9200
./bin/tracestore.exe &    # :9400
./bin/orchestrator.exe &  # :9300
./bin/gateway.exe &       # :8000

# 2. Python 智能层（首次需 venv + 装依赖 + gen-py.sh，见 pyagent/README.md）
PYTHONUTF8=1 PYTHONPATH=pyagent/gen pyagent/.venv/Scripts/python.exe pyagent/server.py &  # :9100

# 3. 打一个任务（注意：Windows 中文用 UTF-8 文件 --data-binary，勿在 GBK 控制台直接 -d '中文'）
curl -X POST http://127.0.0.1:8000/v1/run \
  -H "Content-Type: application/json" -d '{"task":"hello world"}'
# => {"traceId":"trace-...","output":"...\n[tool:reverse] dlrow olleh","status":"OK"}
```
无 DeepSeek key 时智能层走 offline fallback 仍可跑；填 `.env` 的 `DEEPSEEK_API_KEY` 即接真模型。

### 看执行链路
全栈起 5 个服务后，浏览器开 `http://127.0.0.1:8000/viewer`，输入某次请求返回的 `traceId` 即可看到时间线；或终端 `tracectl <traceId>`。两者吃同一份 `GET /v1/trace/{id}` JSON（同一契约、两个渲染器）。

## 密钥

LLM key 走 `.env`（已 gitignore），模板见 `.env.example`，绝不硬编码。

---

## 里程碑状态

- [x] **M0**：工具链 + proto 契约管线（buf lint + generate + go build）端到端可用
- [x] **M1**：4 个 Go 微服务 + Python LangGraph 智能层，端到端跑通一次 task（跨服务 gRPC + trace_id 透传 + 工具调用）
- [x] **M2**：动态路由（research/coding/review）+ Failure/Recovery（L1 retry + L3 model-switch）+ reflect 有界循环；真模型链路已验证
- [x] **M3**：Observability（trace-store + `/viewer` + `tracectl`）、Policy 执行防火墙、Gateway HTTP 注解
- [x] **M4**：外部能力中间件（`/spec`+`/invoke` 契约 + ToolService 连接器，配置即插拔）+ 接入 AI 合约审计器为真实 audit 能力
- 🎉 M0→M4 里程碑全部达成

详见 [ROADMAP.md](ROADMAP.md)。
