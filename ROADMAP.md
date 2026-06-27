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

## M2 — 编排能力加厚 ✅（已完成并端到端验证，对应 v4 第 4/6/10 节）
- [x] Failure/Recovery：L1 retry（瞬时错误白名单+退避）+ L3 model-switch（切 deepseek-reasoner）；Orchestrator 对 Unavailable/DeadlineExceeded 重试；修复 v1「失败谎报 OK」缺陷
- [x] 多 Agent：supervisor 动态路由（LLM 分类+关键词兜底）到 research/coding/review 三 worker
- [x] Control Node：reflect 节点 PASS/RETRY 有界循环（MAX_LOOPS，agentic loop 雏形）

> 验证：离线路由三分类正确；真模型完整链路 status=OK；故障注入可见 retry/switch/真实 FAILED。
> 调优待办：reflect 判官偏严，简单任务也常触发 RETRY 循环（实测一次任务 6 次 LLM 调用），后续可放宽判定或降低 MAX_LOOPS 默认值。

## M3 — 平台能力（对应 v4 第 11/12 节，留接口占位）
- [x] Observability ✅：独立 trace-store 服务(:9400, 内存+JSONL落盘可回放) + orchestrator 最佳努力 capture + gateway 查询 API(`/v1/trace/{id}`、`/v1/traces`) + `/viewer` HTML 链路查看器 + `tracectl` CLI(lipgloss)。凭 trace_id 可还原"请求怎么走的"，同一份 JSON 喂 HTML 与 CLI 两渲染器。已端到端验证(含重启持久化、宕机不影响主链路)。
- [ ] Policy：请求前置 Allow/Deny（执行防火墙雏形）
- [ ] Gateway HTTP：引入 google.api.http 注解 + 第三方 proto vendoring（offline-safe）

## 暂不实现（v4 里、原型阶段留白）
Kafka/NATS 消息总线、Self-Healing、Plugin Runtime(WASM/容器)、ClickHouse、K8s/Canary/Chaos、Graph IR/DSL 自研引擎、Arbiter 仲裁、向量记忆。
均写为占位接口或扩展点，不在原型实现。
