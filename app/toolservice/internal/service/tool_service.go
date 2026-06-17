package service

import (
	"context"
	"encoding/json"
	"fmt"

	toolv1 "github.com/osije/ai-os/api/gen/go/tool/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToolServiceImpl implements toolv1.ToolServiceServer.
type ToolServiceImpl struct {
	toolv1.UnimplementedToolServiceServer
}

func NewToolServiceImpl() *ToolServiceImpl {
	return &ToolServiceImpl{}
}

var tools = []*toolv1.ToolSpec{
	{
		Name:        "echo",
		Description: "原样返回 input_json 内容",
		InputSchema: `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`,
		OutputSchema: `{"type":"object","properties":{"result":{"type":"string"}}}`,
	},
	{
		Name:        "reverse",
		Description: "把 input_json 里 {\"text\": \"...\"} 的 text 字段反转后返回",
		InputSchema: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
		OutputSchema: `{"type":"object","properties":{"result":{"type":"string"}}}`,
	},
}

func (s *ToolServiceImpl) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsReply, error) {
	return &toolv1.ListToolsReply{Tools: tools}, nil
}

func (s *ToolServiceImpl) Invoke(_ context.Context, req *toolv1.InvokeRequest) (*toolv1.InvokeReply, error) {
	switch req.Name {
	case "echo":
		return &toolv1.InvokeReply{
			Ok:         true,
			OutputJson: req.InputJson,
		}, nil

	case "reverse":
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(req.InputJson), &m); err != nil {
			return &toolv1.InvokeReply{Ok: false, Error: fmt.Sprintf("invalid json: %v", err)}, nil
		}
		text, ok := m["text"].(string)
		if !ok {
			return &toolv1.InvokeReply{Ok: false, Error: "missing or non-string 'text' field"}, nil
		}
		runes := []rune(text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		reversed := string(runes)
		out, _ := json.Marshal(map[string]string{"result": reversed})
		return &toolv1.InvokeReply{Ok: true, OutputJson: string(out)}, nil

	default:
		return nil, status.Errorf(codes.NotFound, "tool %q not found", req.Name)
	}
}
