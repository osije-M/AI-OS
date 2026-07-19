package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/osije/ai-os/api/gen/go/agent/v1"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"github.com/osije/ai-os/app/orchestrator/internal/metrics"
	"github.com/osije/ai-os/app/orchestrator/internal/policy"
	"github.com/osije/ai-os/app/orchestrator/internal/runs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// OrchestratorServiceImpl implements orchestratorv1.OrchestratorServer.
type OrchestratorServiceImpl struct {
	orchestratorv1.UnimplementedOrchestratorServer
	agentAddr      string
	maxRetries     int
	backoffMs      int
	traceStoreAddr string
	policyEngine   *policy.Engine
	metrics        *metrics.Metrics // M6-C②，可为 nil（如测试里裸构造），埋点处判空
	runs           *runs.Registry   // M7-2 live 运行注册表（单写者=本服务）
}

func NewOrchestratorServiceImpl(cfg *conf.Config, m *metrics.Metrics) *OrchestratorServiceImpl {
	engine, _ := policy.Load(cfg.PolicyFile) // Load 失败已降级，不会 panic
	gaugeHook := func(active int) {
		if m != nil {
			m.ActiveRuns.Set(float64(active))
		}
	}
	return &OrchestratorServiceImpl{
		agentAddr:      cfg.AgentRuntimeAddr,
		maxRetries:     cfg.MaxRetries,
		backoffMs:      cfg.BackoffMs,
		traceStoreAddr: cfg.TraceStoreAddr,
		policyEngine:   engine,
		metrics:        m,
		runs:           runs.New(0, 0, gaugeHook), // 默认 TTL 5min / 上限 1000
	}
}

// mapReplyState 把 agent 的字符串 status 映射到 RunState 终态。
func mapReplyState(status string) orchestratorv1.RunState {
	switch status {
	case "OK":
		return orchestratorv1.RunState_RUN_STATE_DONE
	case "CANCELLED":
		return orchestratorv1.RunState_RUN_STATE_CANCELLED
	}
	return orchestratorv1.RunState_RUN_STATE_FAILED
}

// isUserCancelled 判定错误是否为"用户 CancelRun 导致的取消"(M7-3)。
// 只认 registry 的 cancelRequested 标记,不猜错误字符串——下游断开/超时同样产生
// Canceled/DeadlineExceeded,语义完全不同。
func (s *OrchestratorServiceImpl) isUserCancelled(traceID string, err error) bool {
	if err == nil {
		return false
	}
	code := status.Code(err)
	return (code == codes.Canceled || code == codes.DeadlineExceeded) && s.runs.CancelRequested(traceID)
}

// CancelRun 取消在飞 run(M7-3):只发信号,状态迁移由持有 handler 完成。
func (s *OrchestratorServiceImpl) CancelRun(_ context.Context, req *orchestratorv1.CancelRunRequest) (*orchestratorv1.CancelRunReply, error) {
	found, st := s.runs.Cancel(req.TraceId)
	log.Printf("[orchestrator] CancelRun trace=%s found=%v state=%v", req.TraceId, found, st)
	return &orchestratorv1.CancelRunReply{Found: found, State: st}, nil
}

// ListRuns 返回 live 注册表快照（M7-2）。
func (s *OrchestratorServiceImpl) ListRuns(_ context.Context, _ *orchestratorv1.ListRunsRequest) (*orchestratorv1.ListRunsReply, error) {
	if s.runs == nil {
		return &orchestratorv1.ListRunsReply{}, nil
	}
	return &orchestratorv1.ListRunsReply{Runs: s.runs.List()}, nil
}

// captureStart 向 TraceStore 写入 run 开始记录（Status=RUNNING，最佳努力；
// 结束时 captureTrace 的完整 trace 借 Save 的 upsert 覆盖它）。
func (s *OrchestratorServiceImpl) captureStart(traceID, task string, startedAt int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(s.traceStoreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] start capture dial error: %v", err)
		return
	}
	defer conn.Close()
	t := &tracev1.Trace{
		TraceId:   traceID,
		Task:      task,
		Status:    "RUNNING",
		StartedAt: startedAt,
	}
	if _, saveErr := tracev1.NewTraceStoreClient(conn).Save(ctx, &tracev1.SaveRequest{Trace: t}); saveErr != nil {
		log.Printf("[orchestrator] start capture save error: %v", saveErr)
	}
}

// recordRequest / recordDenial / recordUsage: nil 安全的埋点封装。
func (s *OrchestratorServiceImpl) recordRequest(route, status string, elapsedMs int64) {
	if s.metrics != nil {
		s.metrics.RecordRequest(route, status, elapsedMs)
	}
}

func (s *OrchestratorServiceImpl) recordDenial() {
	if s.metrics != nil {
		s.metrics.RecordPolicyDenial()
	}
}

func (s *OrchestratorServiceImpl) recordUsage(route string, promptTokens, completionTokens int32) {
	if s.metrics != nil {
		s.metrics.RecordUsage(route, promptTokens, completionTokens)
	}
}

// isRetryable returns true for transient gRPC errors that are safe to retry.
func isRetryable(err error) bool {
	s, _ := status.FromError(err)
	return s.Code() == codes.Unavailable || s.Code() == codes.DeadlineExceeded
}

// retryBackoff sleeps for backoffMs * 2^attempt milliseconds with ±20% jitter,
// but never past the context deadline.
func retryBackoff(ctx context.Context, attempt, backoffMs int) {
	base := backoffMs * (1 << attempt) // backoffMs * 2^attempt
	jitter := rand.Intn(base/5 + 1)   // ±20% jitter (upper bound)
	delay := time.Duration(base+jitter) * time.Millisecond

	// Clamp to remaining context deadline.
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl) - 10*time.Millisecond // small safety margin
		if delay > remaining {
			delay = remaining
		}
	}
	if delay <= 0 {
		return
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func (s *OrchestratorServiceImpl) captureTrace(traceID, task string, reply *agentv1.RunGraphReply, startedAt, endedAt int64, policyStep *tracev1.Step) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(s.traceStoreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] trace capture dial error: %v", err)
		return
	}
	defer conn.Close()

	// 截断 output
	output := reply.Output
	maxOutput := 4096
	if envVal := os.Getenv("TRACE_STORE_MAX_OUTPUT"); envVal != "" {
		if n, err2 := strconv.Atoi(envVal); err2 == nil {
			maxOutput = n
		}
	}
	if len(output) > maxOutput {
		output = output[:maxOutput]
	}

	// 映射 steps：policy step 放在最前（seq=1），原 steps 从 seq=2 开始
	steps := make([]*tracev1.Step, 0, 1+len(reply.Trace))
	steps = append(steps, policyStep)
	for i, nt := range reply.Trace {
		stepStatus := "ok"
		if strings.Contains(strings.ToLower(nt.Summary), "error") ||
			strings.Contains(strings.ToLower(nt.Summary), "[error]") {
			stepStatus = "error"
		}
		steps = append(steps, &tracev1.Step{
			Seq:       int32(i + 2),
			Node:      nt.Node,
			Type:      nt.Type,
			Summary:   nt.Summary,
			LatencyMs: nt.LatencyMs,
			Status:    stepStatus,
		})
	}

	t := &tracev1.Trace{
		TraceId:   traceID,
		Task:      task,
		Status:    reply.Status,
		Route:     reply.Route,
		ElapsedMs: endedAt - startedAt,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Output:    output,
		Steps:     steps,
	}

	client := tracev1.NewTraceStoreClient(conn)
	_, saveErr := client.Save(ctx, &tracev1.SaveRequest{Trace: t})
	if saveErr != nil {
		log.Printf("[orchestrator] trace capture save error: %v", saveErr)
	}
}

// captureSimpleTrace 写一条无 agent 步骤的终态 trace(DENIED / CANCELLED 用)。
func (s *OrchestratorServiceImpl) captureSimpleTrace(traceID, task, traceStatus, reason string, policyStep *tracev1.Step, startedAt, endedAt int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(s.traceStoreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] %s trace capture dial error: %v", traceStatus, err)
		return
	}
	defer conn.Close()
	steps := []*tracev1.Step{}
	if policyStep != nil {
		steps = append(steps, policyStep)
	}
	t := &tracev1.Trace{
		TraceId:   traceID,
		Task:      task,
		Status:    traceStatus,
		Route:     "",
		ElapsedMs: endedAt - startedAt,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Output:    reason,
		Steps:     steps,
	}
	client := tracev1.NewTraceStoreClient(conn)
	if _, saveErr := client.Save(ctx, &tracev1.SaveRequest{Trace: t}); saveErr != nil {
		log.Printf("[orchestrator] %s trace save error: %v", traceStatus, saveErr)
	}
}

func (s *OrchestratorServiceImpl) captureDeniedTrace(traceID, task, reason string, policyStep *tracev1.Step, startedAt, endedAt int64) {
	s.captureSimpleTrace(traceID, task, "DENIED", reason, policyStep, startedAt, endedAt)
}

func (s *OrchestratorServiceImpl) RunTask(ctx context.Context, req *orchestratorv1.RunTaskRequest) (*orchestratorv1.RunTaskReply, error) {
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())
	startedAt := time.Now().UnixMilli()

	// Policy evaluation
	decision := s.policyEngine.Evaluate(req.Task)
	policyStep := &tracev1.Step{
		Seq:     1,
		Node:    "policy",
		Type:    "control",
		Summary: fmt.Sprintf("policy %s", decision.Action),
		Status:  "ok",
	}
	if decision.Action == "deny" {
		policyStep.Summary = fmt.Sprintf("policy deny: %s", decision.Reason)
		// 最佳努力 capture DENIED trace
		endedAt := time.Now().UnixMilli()
		go s.captureDeniedTrace(traceID, req.Task, decision.Reason, policyStep, startedAt, endedAt)
		log.Printf("[orchestrator] policy DENIED trace=%s reason=%s", traceID, decision.Reason)
		s.recordDenial()
		s.recordRequest("", "DENIED", endedAt-startedAt)
		return &orchestratorv1.RunTaskReply{
			TraceId: traceID,
			Status:  "DENIED",
			Output:  decision.Reason,
		}, nil
	}
	if decision.Action == "transform" {
		policyStep.Summary = fmt.Sprintf("policy transform: %s", decision.Reason)
		req = &orchestratorv1.RunTaskRequest{
			Task:   decision.Task,
			Agent:  req.Agent,
			Params: req.Params,
		}
		log.Printf("[orchestrator] policy TRANSFORM trace=%s reason=%s", traceID, decision.Reason)
	}

	// M7-2/3：进入执行即登记（携带取消钩子;deny 不入注册表）+ durable 开始记录
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	s.runs.Register(traceID, req.Task, cancelRun)
	go s.captureStart(traceID, req.Task, startedAt)

	conn, err := grpc.NewClient(s.agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] failed to dial agent runtime %s: %v", s.agentAddr, err)
		s.recordRequest("", "FAILED", time.Now().UnixMilli()-startedAt)
		s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_FAILED, "")
		return &orchestratorv1.RunTaskReply{
			TraceId: traceID,
			Status:  "FAILED",
			Output:  fmt.Sprintf("failed to connect to agent runtime: %v", err),
		}, nil
	}
	defer conn.Close()

	agent := req.Agent
	if agent == "" {
		agent = "supervisor"
	}

	agentClient := agentv1.NewAgentRuntimeClient(conn)
	grpcReq := &agentv1.RunGraphRequest{
		TraceId: traceID,
		Task:    req.Task,
		Agent:   agent,
		Params:  req.Params,
	}

	// Retry loop: total attempts = maxRetries + 1.
	var reply *agentv1.RunGraphReply
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		reply, err = agentClient.RunGraph(runCtx, grpcReq)
		if s.isUserCancelled(traceID, err) {
			break // 用户取消:不重试,直接走取消收尾
		}
		if err == nil {
			break // success
		}
		if !isRetryable(err) {
			break // non-transient error — don't retry
		}
		log.Printf("[orchestrator] RunGraph retryable err (attempt %d): %v", attempt+1, err)
		if attempt < s.maxRetries {
			retryBackoff(ctx, attempt, s.backoffMs)
		}
	}
	endedAt := time.Now().UnixMilli()

	if err != nil {
		// M7-3:用户取消 → CANCELLED 终态(区别于 FAILED)
		if s.isUserCancelled(traceID, err) {
			log.Printf("[orchestrator] RunGraph cancelled by user trace=%s", traceID)
			s.recordRequest("", "CANCELLED", endedAt-startedAt)
			s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_CANCELLED, "")
			go s.captureSimpleTrace(traceID, req.Task, "CANCELLED", "cancelled by user", policyStep, startedAt, endedAt)
			return &orchestratorv1.RunTaskReply{
				TraceId: traceID,
				Status:  "CANCELLED",
				Output:  "cancelled by user",
			}, nil
		}
		log.Printf("[orchestrator] RunGraph final error after %d attempt(s): %v", s.maxRetries+1, err)
		s.recordRequest("", "FAILED", endedAt-startedAt)
		s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_FAILED, "")
		return &orchestratorv1.RunTaskReply{
			TraceId: traceID,
			Status:  "FAILED",
			Output:  fmt.Sprintf("agent runtime error: %v", err),
		}, nil
	}

	// 异步 capture trace（最佳努力，失败不影响结果）
	go s.captureTrace(reply.TraceId, req.Task, reply, startedAt, endedAt, policyStep)

	summaries := make([]string, 0, len(reply.Trace))
	for _, t := range reply.Trace {
		summaries = append(summaries, fmt.Sprintf("[%s/%s] %s (%dms)", t.Node, t.Type, t.Summary, t.LatencyMs))
	}

	s.recordRequest(reply.Route, reply.Status, endedAt-startedAt)
	s.recordUsage(reply.Route, reply.PromptTokens, reply.CompletionTokens)
	s.runs.Finish(traceID, mapReplyState(reply.Status), reply.Route)

	return &orchestratorv1.RunTaskReply{
		TraceId:          reply.TraceId,
		Output:           reply.Output,
		Status:           reply.Status,
		NodeSummaries:    summaries,
		PromptTokens:     reply.PromptTokens,
		CompletionTokens: reply.CompletionTokens,
	}, nil
}

// RunTaskStream streams task execution events to the caller (server-side streaming).
// Flow: policy → agent.RunGraphStream (recv loop, map events) → capture trace on done.
func (s *OrchestratorServiceImpl) RunTaskStream(req *orchestratorv1.RunTaskRequest, stream grpc.ServerStreamingServer[orchestratorv1.StreamEvent]) error {
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())
	startedAt := time.Now().UnixMilli()

	// Policy evaluation
	decision := s.policyEngine.Evaluate(req.Task)
	policyStep := &tracev1.Step{
		Seq:     1,
		Node:    "policy",
		Type:    "control",
		Summary: fmt.Sprintf("policy %s", decision.Action),
		Status:  "ok",
	}

	if decision.Action == "deny" {
		policyStep.Summary = fmt.Sprintf("policy deny: %s", decision.Reason)
		endedAt := time.Now().UnixMilli()
		// Best-effort: send DENIED done event
		_ = stream.Send(&orchestratorv1.StreamEvent{
			Type:    "done",
			TraceId: traceID,
			Final: &orchestratorv1.RunTaskReply{
				TraceId: traceID,
				Status:  "DENIED",
				Output:  decision.Reason,
			},
		})
		go s.captureDeniedTrace(traceID, req.Task, decision.Reason, policyStep, startedAt, endedAt)
		log.Printf("[orchestrator] stream policy DENIED trace=%s reason=%s", traceID, decision.Reason)
		s.recordDenial()
		s.recordRequest("", "DENIED", endedAt-startedAt)
		return nil
	}

	task := req.Task
	agent := req.Agent
	if decision.Action == "transform" {
		policyStep.Summary = fmt.Sprintf("policy transform: %s", decision.Reason)
		task = decision.Task
		log.Printf("[orchestrator] stream policy TRANSFORM trace=%s reason=%s", traceID, decision.Reason)
	}
	if agent == "" {
		agent = "supervisor"
	}

	// M7-2/3：进入执行即登记(携带取消钩子)+ durable 开始记录（best-effort）
	runCtx, cancelRun := context.WithCancel(stream.Context())
	defer cancelRun()
	s.runs.Register(traceID, task, cancelRun)
	go s.captureStart(traceID, task, startedAt)
	// 兜底:任何提前 return(下游 sendErr、recv 异常)都保证离开 RUNNING;
	// 正常路径先 Finish(DONE/FAILED/CANCELLED),此 defer 因终态幂等而无操作。
	defer s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_FAILED, "")

	conn, err := grpc.NewClient(s.agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] stream failed to dial agent runtime %s: %v", s.agentAddr, err)
		_ = stream.Send(&orchestratorv1.StreamEvent{
			Type:    "error",
			TraceId: traceID,
			Content: fmt.Sprintf("failed to connect to agent runtime: %v", err),
		})
		s.recordRequest("", "FAILED", time.Now().UnixMilli()-startedAt)
		s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_FAILED, "")
		return nil
	}
	defer conn.Close()

	agentClient := agentv1.NewAgentRuntimeClient(conn)
	agentStream, err := agentClient.RunGraphStream(runCtx, &agentv1.RunGraphRequest{
		TraceId: traceID,
		Task:    task,
		Agent:   agent,
		Params:  req.Params,
	})
	if err != nil {
		log.Printf("[orchestrator] stream RunGraphStream call error: %v", err)
		_ = stream.Send(&orchestratorv1.StreamEvent{
			Type:    "error",
			TraceId: traceID,
			Content: fmt.Sprintf("agent stream error: %v", err),
		})
		s.recordRequest("", "FAILED", time.Now().UnixMilli()-startedAt)
		s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_FAILED, "")
		return nil
	}

	var fullOutput string
	var agentTrace []*agentv1.NodeTrace
	endedAt := startedAt

	for {
		ev, recvErr := agentStream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			// M7-3:用户取消 → 发 CANCELLED 终结事件而非 error
			if s.isUserCancelled(traceID, recvErr) {
				endedAt = time.Now().UnixMilli()
				log.Printf("[orchestrator] stream cancelled by user trace=%s", traceID)
				_ = stream.Send(&orchestratorv1.StreamEvent{
					Type:    "done",
					TraceId: traceID,
					Final: &orchestratorv1.RunTaskReply{
						TraceId: traceID,
						Status:  "CANCELLED",
						Output:  "cancelled by user",
					},
				})
				s.recordRequest("", "CANCELLED", endedAt-startedAt)
				s.runs.Finish(traceID, orchestratorv1.RunState_RUN_STATE_CANCELLED, "")
				go s.captureSimpleTrace(traceID, task, "CANCELLED", "cancelled by user", policyStep, startedAt, endedAt)
				break
			}
			log.Printf("[orchestrator] stream recv error: %v", recvErr)
			_ = stream.Send(&orchestratorv1.StreamEvent{
				Type:    "error",
				TraceId: traceID,
				Content: recvErr.Error(),
			})
			s.recordRequest("", "FAILED", time.Now().UnixMilli()-startedAt)
			break
		}

		switch ev.Type {
		case "token":
			s.runs.Touch(traceID) // 带内心跳
			fullOutput += ev.Content
			if sendErr := stream.Send(&orchestratorv1.StreamEvent{
				Type:    ev.Type,
				TraceId: traceID,
				Node:    ev.Node,
				Content: ev.Content,
			}); sendErr != nil {
				return sendErr
			}
		case "node":
			s.runs.MarkNode(traceID, ev.Node) // 当前节点 + 带内心跳
			if sendErr := stream.Send(&orchestratorv1.StreamEvent{
				Type:    ev.Type,
				TraceId: traceID,
				Node:    ev.Node,
				Content: ev.Content,
			}); sendErr != nil {
				return sendErr
			}
		case "done":
			endedAt = time.Now().UnixMilli()
			// Prefer agent's Final.Output, fall back to accumulated fullOutput
			output := fullOutput
			agentStatus := "OK"
			var route string
			var promptTokens, completionTokens int32
			if ev.Final != nil {
				if ev.Final.Output != "" {
					output = ev.Final.Output
				}
				if ev.Final.Status != "" {
					agentStatus = ev.Final.Status
				}
				route = ev.Final.Route
				agentTrace = ev.Final.Trace
				promptTokens = ev.Final.PromptTokens
				completionTokens = ev.Final.CompletionTokens
			}

			// Build NodeSummaries
			summaries := make([]string, 0, len(agentTrace))
			for _, nt := range agentTrace {
				summaries = append(summaries, fmt.Sprintf("[%s/%s] %s (%dms)", nt.Node, nt.Type, nt.Summary, nt.LatencyMs))
			}

			finalReply := &orchestratorv1.RunTaskReply{
				TraceId:          traceID,
				Output:           output,
				Status:           agentStatus,
				NodeSummaries:    summaries,
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
			}
			if sendErr := stream.Send(&orchestratorv1.StreamEvent{
				Type:    "done",
				TraceId: traceID,
				Final:   finalReply,
			}); sendErr != nil {
				return sendErr
			}

			s.recordRequest(route, agentStatus, endedAt-startedAt)
			s.recordUsage(route, promptTokens, completionTokens)
			s.runs.Finish(traceID, mapReplyState(agentStatus), route)

			// Best-effort capture trace
			syntheticReply := &agentv1.RunGraphReply{
				TraceId: traceID,
				Output:  output,
				Status:  agentStatus,
				Route:   route,
				Trace:   agentTrace,
			}
			go s.captureTrace(traceID, task, syntheticReply, startedAt, endedAt, policyStep)

		case "error":
			if sendErr := stream.Send(&orchestratorv1.StreamEvent{
				Type:    "error",
				TraceId: traceID,
				Content: ev.Content,
			}); sendErr != nil {
				return sendErr
			}
		default:
			// forward unknown event types as-is
			if sendErr := stream.Send(&orchestratorv1.StreamEvent{
				Type:    ev.Type,
				TraceId: traceID,
				Node:    ev.Node,
				Content: ev.Content,
			}); sendErr != nil {
				return sendErr
			}
		}
	}

	log.Printf("[orchestrator] stream done trace=%s elapsed=%dms", traceID, endedAt-startedAt)
	return nil
}
