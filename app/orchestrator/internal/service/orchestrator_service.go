package service

import (
	"context"
	"fmt"
	"log"
	"time"

	agentv1 "github.com/osije/ai-os/api/gen/go/agent/v1"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	"github.com/osije/ai-os/app/orchestrator/internal/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// OrchestratorServiceImpl implements orchestratorv1.OrchestratorServer.
type OrchestratorServiceImpl struct {
	orchestratorv1.UnimplementedOrchestratorServer
	agentAddr string
}

func NewOrchestratorServiceImpl(cfg *conf.Config) *OrchestratorServiceImpl {
	return &OrchestratorServiceImpl{agentAddr: cfg.AgentRuntimeAddr}
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
	reply, err := agentClient.RunGraph(ctx, &agentv1.RunGraphRequest{
		TraceId: traceID,
		Task:    req.Task,
		Agent:   agent,
		Params:  req.Params,
	})
	if err != nil {
		log.Printf("[orchestrator] RunGraph error: %v", err)
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
		TraceId:      reply.TraceId,
		Output:       reply.Output,
		Status:       reply.Status,
		NodeSummaries: summaries,
	}, nil
}
