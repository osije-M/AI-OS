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
- [x] Policy ✅：orchestrator 执行前置 Allow/Deny/Transform(configs/policy.yaml, version 化)；deny 不调 LLM、status=DENIED；policy 决策记入 trace(被拦请求 viewer 可见)；安全降级。
- [x] Gateway HTTP ✅：google.api.http 注解生成 `POST /v1/run`(protoc-gen-go-http)；第三方 proto 从 kratos 模块缓存 vendor 到 third_party/(offline-safe)；buf v2 用 `buf generate api/proto` 限定不污染 google/api。trace/traces/viewer 仍手写路由作对照。注：/v1/run 响应改 camelCase(traceId)。

> **M3 三项全部完成。**

## M4 — 外部能力中间件 + 接入真实 OSS ✅
- [x] 通用「外部能力」契约：`GET /spec` + `POST /invoke`（刻意对齐 tool.proto，可无缝并入 Tool Mesh）
- [x] ToolService 外部连接器：读 `EXTERNAL_TOOLS` 配置，ListTools 合并外部 /spec、Invoke 代理外部 /invoke（按需刷新映射）；**配置即插拔**
- [x] 接入 **AI 合约审计器**：在审计器仓库新增 `cmd/auditserver`（不改其内部代码），暴露标准契约；Python 加 audit 路由 + audit worker 经 Tool Mesh 调它
- [x] 端到端验证：审计任务路由到 audit → 经 Mesh 调外部审计器 → 返回真实混合管线结论（重入 [VULNERABLE] 95%）→ 全程入 trace；插拔与降级均验过

> **意义**：证明"任意 OSS 套 /spec+/invoke 薄壳 + 配置加 URL 即可接入，AI-OS 零代码改动"。M0→M4 全部达成。

## M5 — Token 级流式输出 ✅
- [x] 契约：agent/orchestrator 各加流式 RPC（RunGraphStream/RunTaskStream）+ StreamEvent（node/token/done/error），unary 保留
- [x] Python：run_graph_stream 生成器，DeepSeek stream=True 逐 token 产出（L3 切换）；离线分块模拟；audit 退化为整段
- [x] Go：orchestrator RunTaskStream 转发（先过 Policy，done 后 capture trace）；gateway `POST /v1/run/stream` SSE（每事件 flush）
- [x] 流式路径 = 路由 + 流式作答（不跑 reflect，避免流完又重做的 UX 冲突）
- [x] 端到端验证：curl -N 真模型见 91 个 token 增量、done 带完整答案、trace 可查；deny 即拒、unary 回归、离线模拟均过

> 至此 M0→M5 全部达成。"演示→可信作品"补强里，流式输出已落地。

## M6 — 工程化补强（"演示→可信作品"）

- [x] 切片1：`docker-compose.yml` 一键起 5 服务（无 auditserver 时 `full` profile 可选接入）
- [x] 切片2：Go/Python 单元测试补全 + GitHub Actions CI（go / python 两个 job）
- [x] 切片3：架构图 SVG + README 首屏
- [x] M6-B①：TraceStore 持久化从 JSONL 换成 SQLite（可查询/可统计）
- [x] M6-B②：Gateway API Key 鉴权 + 令牌桶限流
- [x] M6-B③：`evalctl` 评测框架（端到端基准 + 独立 `eval.db`，`-strict` 预留 CI 门禁用途）
- [x] M6-C①：eval 回归门禁接入 CI——新增 `eval/suite-offline.yaml`（12 条确定性用例）+
      `pyagent/tests/test_offline_routing.py`（关键词路由秒级快挂）+ ci.yml 新增 `eval-offline` job
      （compose 起全栈 → 离线基准跑 `evalctl -strict` → 失败输出 compose logs → 无论成败 compose down）。
      门禁首跑即抓到存量缺陷：agentruntime 镜像依赖本地 gitignored 的 `pyagent/gen/`，干净 checkout
      起不来——已改为镜像内自生成 stubs（自洽构建），"新 clone compose up 即跑"自此真实成立
- [x] M6-C②：运行时指标观测——契约加 token 用量字段（pyagent 聚合真实 usage：router+worker+reflect
      累计，stream 走 include_usage 带降级；agent→orchestrator→gateway 逐层透传），orchestrator 暴露
      Prometheus `/metrics`（:9301）：请求量/延迟分布/policy 拒绝/token 用量/估算成本 5 组 `aios_*` 指标

## 暂不实现（v4 里、原型阶段留白）
Kafka/NATS 消息总线、Self-Healing、Plugin Runtime(WASM/容器)、ClickHouse、K8s/Canary/Chaos、Graph IR/DSL 自研引擎、Arbiter 仲裁、向量记忆。
均写为占位接口或扩展点，不在原型实现。
