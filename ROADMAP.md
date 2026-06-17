# ROADMAP

把 v4 愿景切成可落地的纵向切片，由薄到厚。每个里程碑都能跑、都能指回 v4。

## M0 — 契约与工具链 ✅（已完成）
- [x] Go/Kratos/buf 工具链装好并验证
- [x] 4 份 proto 契约（gateway/orchestrator/agent/tool）
- [x] buf lint clean + 生成 Go 代码 + go build 通过

## M1 — 三服务骨架跑通 ✅（已完成并端到端验证）
- [x] ToolService（Go/Kratos）：`ListTools` + `Invoke`，内置 echo / reverse 两个工具
- [x] AgentRuntime（Python/LangGraph）：gRPC server + supervisor→worker 图，worker 通过 ToolService 调工具；无 key 走 offline fallback
- [x] Orchestrator（Go/Kratos）：`RunTask` → 调 AgentRuntime.RunGraph，汇总轨迹
- [x] Gateway（Go/Kratos）：HTTP `POST /v1/run` → 调 Orchestrator
- [x] 端到端：curl 一个 task，Gateway→Orchestrator→AgentRuntime→ToolService 全程走通，trace_id 透传
- [x] wire 依赖注入分层、conf 配置

> 注：wire 与 protobuf v2 模块缓存有兼容问题导致 `wire gen` 报 internal error，三服务的 `wire_gen.go` 暂为手写（与生成物等价），`wire.go` 模板保留。M2 可重新评估。

## M2 — 编排能力加厚（对应 v4 第 4/6/10 节）
- [ ] Failure/Recovery：L1 retry + L3 model-switch
- [ ] 多 Agent：supervisor 路由到 research/coding/review 多 worker
- [ ] Control Node：IF / LOOP

## M3 — 平台能力（对应 v4 第 11/12 节，留接口占位）
- [ ] Observability：结构化 trace → 文件/简单存储（先不上 ClickHouse）
- [ ] Policy：请求前置 Allow/Deny（执行防火墙雏形）
- [ ] Gateway HTTP：引入 google.api.http 注解 + 第三方 proto vendoring（offline-safe）

## 暂不实现（v4 里、原型阶段留白）
Kafka/NATS 消息总线、Self-Healing、Plugin Runtime(WASM/容器)、ClickHouse、K8s/Canary/Chaos、Graph IR/DSL 自研引擎、Arbiter 仲裁、向量记忆。
均写为占位接口或扩展点，不在原型实现。
