package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	gatewayv1 "github.com/osije/ai-os/api/gen/go/gateway/v1"
	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/gateway/internal/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// GatewayServiceImpl implements gatewayv1.GatewayServer and handles HTTP routing.
type GatewayServiceImpl struct {
	gatewayv1.UnimplementedGatewayServer
	orchAddr  string
	traceAddr string
}

func NewGatewayServiceImpl(cfg *conf.Config) *GatewayServiceImpl {
	return &GatewayServiceImpl{
		orchAddr:  cfg.OrchestratorAddr,
		traceAddr: cfg.TraceStoreAddr,
	}
}

func (g *GatewayServiceImpl) dialOrchestrator() (*grpc.ClientConn, orchestratorv1.OrchestratorClient, error) {
	conn, err := grpc.NewClient(g.orchAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return conn, orchestratorv1.NewOrchestratorClient(conn), nil
}

func (g *GatewayServiceImpl) dialTraceStore() (*grpc.ClientConn, tracev1.TraceStoreClient, error) {
	conn, err := grpc.NewClient(g.traceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return conn, tracev1.NewTraceStoreClient(conn), nil
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

// HandleGetTrace handles GET /v1/trace/{id}
func (g *GatewayServiceImpl) HandleGetTrace(ctx khttp.Context) error {
	id := ctx.Vars().Get("id")
	conn, client, err := g.dialTraceStore()
	if err != nil {
		return ctx.JSON(502, map[string]string{"error": err.Error()})
	}
	defer conn.Close()
	rCtx, cancel := context.WithTimeout(ctx.Request().Context(), 5*time.Second)
	defer cancel()
	reply, err := client.Get(rCtx, &tracev1.GetRequest{TraceId: id})
	if err != nil {
		return ctx.JSON(502, map[string]string{"error": err.Error()})
	}
	if !reply.Found {
		return ctx.JSON(404, map[string]string{"error": "not found"})
	}
	b, _ := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(reply.Trace)
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(200)
	ctx.Response().Write(b)
	return nil
}

// HandleListTraces handles GET /v1/traces?limit=20
func (g *GatewayServiceImpl) HandleListTraces(ctx khttp.Context) error {
	limitStr := ctx.Query().Get("limit")
	limit := int32(20)
	if n, err := strconv.ParseInt(limitStr, 10, 32); err == nil {
		limit = int32(n)
	}
	conn, client, err := g.dialTraceStore()
	if err != nil {
		return ctx.JSON(502, map[string]string{"error": err.Error()})
	}
	defer conn.Close()
	rCtx, cancel := context.WithTimeout(ctx.Request().Context(), 5*time.Second)
	defer cancel()
	reply, err := client.List(rCtx, &tracev1.ListRequest{Limit: limit})
	if err != nil {
		return ctx.JSON(502, map[string]string{"error": err.Error()})
	}
	// 手动序列化成 {"traces": [...]}
	marshaler := protojson.MarshalOptions{EmitUnpopulated: true}
	parts := make([]json.RawMessage, 0, len(reply.Traces))
	for _, t := range reply.Traces {
		b, _ := marshaler.Marshal(t)
		parts = append(parts, json.RawMessage(b))
	}
	out, _ := json.Marshal(map[string]interface{}{"traces": parts})
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(200)
	ctx.Response().Write(out)
	return nil
}

// HandleRunStream handles POST /v1/run/stream — SSE streaming endpoint.
// Parses {task, agent} from body, calls orchestrator.RunTaskStream, and writes
// Server-Sent Events with flush after every event.
func (g *GatewayServiceImpl) HandleRunStream(ctx khttp.Context) error {
	// Parse request body
	var body struct {
		Task  string `json:"task"`
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(ctx.Request().Body).Decode(&body); err != nil {
		return ctx.JSON(400, map[string]string{"error": "invalid request body: " + err.Error()})
	}
	if body.Task == "" {
		return ctx.JSON(400, map[string]string{"error": "task is required"})
	}

	// Set SSE response headers
	w := ctx.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if present
	w.WriteHeader(http.StatusOK)

	// Get flusher — must flush after each event or browser sees nothing until close
	flusher, canFlush := w.(http.Flusher)

	// writeEvent writes one SSE data line and flushes
	writeEvent := func(data []byte) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	}

	// Dial orchestrator
	conn, orcClient, err := g.dialOrchestrator()
	if err != nil {
		errData, _ := json.Marshal(map[string]string{"type": "error", "content": err.Error()})
		writeEvent(errData)
		return nil
	}
	defer conn.Close()

	orchStream, err := orcClient.RunTaskStream(ctx.Request().Context(), &orchestratorv1.RunTaskRequest{
		Task:  body.Task,
		Agent: body.Agent,
	})
	if err != nil {
		errData, _ := json.Marshal(map[string]string{"type": "error", "content": err.Error()})
		writeEvent(errData)
		return nil
	}

	for {
		ev, recvErr := orchStream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			errData, _ := json.Marshal(map[string]string{"type": "error", "content": recvErr.Error()})
			writeEvent(errData)
			break
		}

		var payload map[string]interface{}
		switch ev.Type {
		case "done":
			payload = map[string]interface{}{
				"type":     "done",
				"trace_id": ev.TraceId,
			}
			if ev.Final != nil {
				payload["output"] = ev.Final.Output
				payload["status"] = ev.Final.Status
				// route is not in orchestrator RunTaskReply, set empty
				payload["route"] = ""
			}
		case "token":
			payload = map[string]interface{}{
				"type":     "token",
				"trace_id": ev.TraceId,
				"node":     ev.Node,
				"content":  ev.Content,
			}
		case "node":
			payload = map[string]interface{}{
				"type":     "node",
				"trace_id": ev.TraceId,
				"node":     ev.Node,
				"content":  ev.Content,
			}
		case "error":
			payload = map[string]interface{}{
				"type":    "error",
				"content": ev.Content,
			}
		default:
			payload = map[string]interface{}{
				"type":     ev.Type,
				"trace_id": ev.TraceId,
				"node":     ev.Node,
				"content":  ev.Content,
			}
		}

		data, _ := json.Marshal(payload)
		writeEvent(data)
	}

	return nil
}

// HandleViewer handles GET /viewer - serves tools/trace-viewer/index.html
func (g *GatewayServiceImpl) HandleViewer(ctx khttp.Context) error {
	htmlPath := os.Getenv("VIEWER_HTML")
	if htmlPath == "" {
		htmlPath = "tools/trace-viewer/index.html"
	}
	b, err := os.ReadFile(htmlPath)
	if err != nil {
		ctx.Response().Header().Set("Content-Type", "text/plain")
		ctx.Response().WriteHeader(200)
		ctx.Response().Write([]byte("trace viewer not available yet (index.html not found)"))
		return nil
	}
	ctx.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	ctx.Response().WriteHeader(200)
	ctx.Response().Write(b)
	return nil
}
