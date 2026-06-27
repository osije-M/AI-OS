package service

import (
	"context"
	"fmt"
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
	"github.com/osije/ai-os/app/orchestrator/internal/policy"
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
}

func NewOrchestratorServiceImpl(cfg *conf.Config) *OrchestratorServiceImpl {
	engine, _ := policy.Load(cfg.PolicyFile) // Load 失败已降级，不会 panic
	return &OrchestratorServiceImpl{
		agentAddr:      cfg.AgentRuntimeAddr,
		maxRetries:     cfg.MaxRetries,
		backoffMs:      cfg.BackoffMs,
		traceStoreAddr: cfg.TraceStoreAddr,
		policyEngine:   engine,
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

func (s *OrchestratorServiceImpl) captureDeniedTrace(traceID, task, reason string, policyStep *tracev1.Step, startedAt, endedAt int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(s.traceStoreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] denied trace capture dial error: %v", err)
		return
	}
	defer conn.Close()
	t := &tracev1.Trace{
		TraceId:   traceID,
		Task:      task,
		Status:    "DENIED",
		Route:     "",
		ElapsedMs: endedAt - startedAt,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Output:    reason,
		Steps:     []*tracev1.Step{policyStep},
	}
	client := tracev1.NewTraceStoreClient(conn)
	_, saveErr := client.Save(ctx, &tracev1.SaveRequest{Trace: t})
	if saveErr != nil {
		log.Printf("[orchestrator] denied trace save error: %v", saveErr)
	}
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

	conn, err := grpc.NewClient(s.agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[orchestrator] failed to dial agent runtime %s: %v", s.agentAddr, err)
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
		reply, err = agentClient.RunGraph(ctx, grpcReq)
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
		log.Printf("[orchestrator] RunGraph final error after %d attempt(s): %v", s.maxRetries+1, err)
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

	return &orchestratorv1.RunTaskReply{
		TraceId:       reply.TraceId,
		Output:        reply.Output,
		Status:        reply.Status,
		NodeSummaries: summaries,
	}, nil
}
