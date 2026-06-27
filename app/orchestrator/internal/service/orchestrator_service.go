package service

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	agentv1 "github.com/osije/ai-os/api/gen/go/agent/v1"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// OrchestratorServiceImpl implements orchestratorv1.OrchestratorServer.
type OrchestratorServiceImpl struct {
	orchestratorv1.UnimplementedOrchestratorServer
	agentAddr  string
	maxRetries int
	backoffMs  int
}

func NewOrchestratorServiceImpl(cfg *conf.Config) *OrchestratorServiceImpl {
	return &OrchestratorServiceImpl{
		agentAddr:  cfg.AgentRuntimeAddr,
		maxRetries: cfg.MaxRetries,
		backoffMs:  cfg.BackoffMs,
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

func (s *OrchestratorServiceImpl) RunTask(ctx context.Context, req *orchestratorv1.RunTaskRequest) (*orchestratorv1.RunTaskReply, error) {
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())

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

	if err != nil {
		log.Printf("[orchestrator] RunGraph final error after %d attempt(s): %v", s.maxRetries+1, err)
		return &orchestratorv1.RunTaskReply{
			TraceId: traceID,
			Status:  "FAILED",
			Output:  fmt.Sprintf("agent runtime error: %v", err),
		}, nil
	}

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
