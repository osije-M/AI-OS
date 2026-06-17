package service

import (
	"context"
	"log"

	gatewayv1 "github.com/osije/ai-os/api/gen/go/gateway/v1"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GatewayServiceImpl implements gatewayv1.GatewayServer and handles HTTP routing.
type GatewayServiceImpl struct {
	gatewayv1.UnimplementedGatewayServer
	orchAddr string
}

func NewGatewayServiceImpl(cfg *conf.Config) *GatewayServiceImpl {
	return &GatewayServiceImpl{orchAddr: cfg.OrchestratorAddr}
}

func (g *GatewayServiceImpl) dialOrchestrator() (*grpc.ClientConn, orchestratorv1.OrchestratorClient, error) {
	conn, err := grpc.NewClient(g.orchAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return conn, orchestratorv1.NewOrchestratorClient(conn), nil
}

// Run implements gateway.v1.Gateway gRPC method.
func (g *GatewayServiceImpl) Run(ctx context.Context, req *gatewayv1.RunRequest) (*gatewayv1.RunReply, error) {
	conn, client, err := g.dialOrchestrator()
	if err != nil {
		log.Printf("[gateway] dial orchestrator error: %v", err)
		return &gatewayv1.RunReply{Status: "FAILED", Output: err.Error()}, nil
	}
	defer conn.Close()

	reply, err := client.RunTask(ctx, &orchestratorv1.RunTaskRequest{
		Task:  req.Task,
		Agent: req.Agent,
	})
	if err != nil {
		return &gatewayv1.RunReply{Status: "FAILED", Output: err.Error()}, nil
	}
	return &gatewayv1.RunReply{
		TraceId: reply.TraceId,
		Output:  reply.Output,
		Status:  reply.Status,
	}, nil
}

// runRequest is the HTTP body for POST /v1/run.
type runRequest struct {
	Task  string `json:"task"`
	Agent string `json:"agent"`
}

// runReply is the HTTP response for POST /v1/run.
type runReply struct {
	TraceID string `json:"trace_id"`
	Output  string `json:"output"`
	Status  string `json:"status"`
}

// HandleRun handles POST /v1/run via Kratos HTTP context.
func (g *GatewayServiceImpl) HandleRun(ctx khttp.Context) error {
	var req runRequest
	if err := ctx.Bind(&req); err != nil {
		return err
	}

	conn, client, err := g.dialOrchestrator()
	if err != nil {
		log.Printf("[gateway] dial orchestrator error: %v", err)
		return ctx.JSON(502, &runReply{Status: "FAILED", Output: err.Error()})
	}
	defer conn.Close()

	reply, err := client.RunTask(ctx.Request().Context(), &orchestratorv1.RunTaskRequest{
		Task:  req.Task,
		Agent: req.Agent,
	})
	if err != nil {
		log.Printf("[gateway] RunTask error: %v", err)
		return ctx.JSON(502, &runReply{Status: "FAILED", Output: err.Error()})
	}
	return ctx.JSON(200, &runReply{
		TraceID: reply.TraceId,
		Output:  reply.Output,
		Status:  reply.Status,
	})
}
