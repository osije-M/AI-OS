# AI-OS — LangGraph × Kratos 的 Agent 编排平台

> **一句话**：用 **Go/Kratos 写确定性控制面**（三个微服务）+ **Python/LangGraph 写非确定性智能层**，把 LLM 能力工程化为可控、可扩展、可观测的分布式 Agent 编排系统。

对应架构愿景见 [`AI-Agent-Framework-AIOS-Architecture-v4.md`](AI-Agent-Framework-AIOS-Architecture-v4.md)（企业级蓝图）。本仓库是其**最小可运行纵向切片**的原型实现，逐步加厚。

---

## 核心理念：双流模型

v4 第 1 节的 `Deterministic Execution Kernel + Non-deterministic Intelligence Layer` 直接映射到技术栈：

| 流 | 职责 | 实现 |
|---|---|---|
| **Control Flow**（确定性、可 replay） | 路由 / 调度 / 重试 / 恢复 | **Go + Kratos** 微服务 |
| **Data Flow**（概率性、可变化） | LLM 推理 / 工具调用 / 状态记忆 | **Python + LangGraph** |

> Control 必须稳定，Data 可以变化。

---

## 架构与服务拓扑

```
Client
  │ HTTP/gRPC
  ▼
┌─────────────┐   gRPC   ┌──────────────────┐   gRPC   ┌────────────────────────┐
│  Gateway    │ ───────▶ │   Orchestrator   │ ───────▶ │  AgentRuntime (Python) │
│ (Go/Kratos) │          │   (Go/Kratos)    │          │   LangGraph 编排图      │
└─────────────┘          └──────────────────┘          └───────────┬────────────┘
  入口/鉴权/限流            计划/调度/汇总轨迹                          │ gRPC
                                                                     ▼
                                                        ┌────────────────────────┐
                                                        │  ToolService (Go)      │
                                                        │  Tool Mesh / 工具即服务 │
                                                        └────────────────────────┘
```

- **Gateway**（Go/Kratos）：对外入口，转发到 Orchestrator。
- **Orchestrator**（Go/Kratos）：编排控制面，驱动一次执行，调用 Python 智能层跑图，汇总轨迹。
- **AgentRuntime**（Python/LangGraph）：实际的多 Agent 图（supervisor→worker），被 Orchestrator 调用；工具走 ToolService。
- **ToolService**（Go/Kratos）：Tool Mesh，"工具即服务"，对外暴露工具发现与调用。

契约先行：所有服务接口定义在 [`api/proto/`](api/proto/)，Go 与 Python 共享同一份 proto。

---

## 目录结构

```
.
├── api/proto/            # 唯一契约源（4 份 proto，Go/Python 共享）
│   ├── gateway/v1/
│   ├── orchestrator/v1/
│   ├── agent/v1/         # AgentRuntime：Python 实现、Go 调用
│   └── tool/v1/
├── api/gen/go/           # buf 生成的 Go 代码
├── app/                  # 三个 Go/Kratos 微服务（gateway/orchestrator/toolservice）
├── pyagent/              # Python/LangGraph 智能层（gRPC server + 编排图）
├── configs/              # 各服务配置（YAML）
├── scripts/              # gen.sh(Go) / gen-py.sh(Python) 代码生成
├── buf.yaml buf.gen.yaml # proto 工具链配置
└── AI-...-v4.md          # 架构愿景
```

---

## 工具链（本机已验证）

Go 1.22(+自动 go1.24) · **Kratos v2.9.2** · **buf 1.47.2**（替代 protoc，纯 Go）· protoc-gen-go/grpc/http/errors · wire · Python 3.12

> 本机网络坑与规避：见个人知识库（goproxy.cn 挂→用 goproxy.io；protoc 二进制装不了→用 buf）。

## 代码生成

```bash
bash scripts/gen.sh      # 生成 Go（buf）
bash scripts/gen-py.sh   # 生成 Python stubs（grpcio-tools）
```

## 密钥

LLM key 走 `.env`（已 gitignore），模板见 `.env.example`，绝不硬编码。

---

## 快速跑通（端到端已验证）

```bash
# 1. Go 三服务
go build -o bin/toolservice.exe  ./app/toolservice/cmd/toolservice
go build -o bin/orchestrator.exe ./app/orchestrator/cmd/orchestrator
go build -o bin/gateway.exe      ./app/gateway/cmd/gateway
./bin/toolservice.exe &   # :9200
./bin/orchestrator.exe &  # :9300
./bin/gateway.exe &       # :8000

# 2. Python 智能层（首次需 venv + 装依赖 + gen-py.sh，见 pyagent/README.md）
PYTHONUTF8=1 PYTHONPATH=pyagent/gen pyagent/.venv/Scripts/python.exe pyagent/server.py &  # :9100

# 3. 打一个任务，链路 Gateway→Orchestrator→AgentRuntime→ToolService 全程走通
curl -X POST http://127.0.0.1:8000/v1/run \
  -H "Content-Type: application/json" -d '{"task":"hello world","agent":"supervisor"}'
# => {"trace_id":"...","output":"[offline] echo: hello world\n[tool:reverse] dlrow olleh","status":"OK"}
```
无 DeepSeek key 时智能层走 offline fallback 仍可跑；填 `.env` 的 `DEEPSEEK_API_KEY` 即接真模型。

## 状态

- [x] M0：工具链打通、proto 契约管线（buf lint + generate + go build）端到端可用
- [x] M1：三个 Go/Kratos 微服务骨架（gateway/orchestrator/toolservice）+ Python LangGraph 智能层（supervisor→worker）
- [x] M1：端到端跑通一次 task（含跨服务 gRPC + trace_id 透传 + 工具调用）
- [x] M2：动态路由（research/coding/review）+ Failure/Recovery（L1 retry + L3 model-switch）+ reflect 有界循环；真模型链路已验证
- [x] M3 Observability：trace-store 服务(:9400) + capture + 查询 API + `/viewer` 网页 + `tracectl` CLI；凭 trace_id 还原执行链路，已端到端验证
- [ ] M3 续：Policy 防火墙、Gateway HTTP 注解（见 ROADMAP）

### 看执行链路（M3）
全栈起 5 个服务后，浏览器开 `http://127.0.0.1:8000/viewer`，输入某次请求返回的 `trace_id` 即可看到时间线；或终端 `tracectl <trace_id>`。两者吃同一份 `GET /v1/trace/{id}` JSON。

详见 [ROADMAP.md](ROADMAP.md)。
