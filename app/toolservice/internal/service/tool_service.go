package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	toolv1 "github.com/osije/ai-os/api/gen/go/tool/v1"
	"github.com/osije/ai-os/app/toolservice/internal/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToolServiceImpl implements toolv1.ToolServiceServer.
type ToolServiceImpl struct {
	toolv1.UnimplementedToolServiceServer

	conf *conf.Config

	mu           sync.RWMutex
	externalURLs map[string]string // toolName -> baseURL
}

func NewToolServiceImpl(cfg *conf.Config) *ToolServiceImpl {
	return &ToolServiceImpl{
		conf:         cfg,
		externalURLs: make(map[string]string),
	}
}

var builtinTools = []*toolv1.ToolSpec{
	{
		Name:         "echo",
		Description:  "原样返回 input_json 内容",
		InputSchema:  `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`,
		OutputSchema: `{"type":"object","properties":{"result":{"type":"string"}}}`,
	},
	{
		Name:         "reverse",
		Description:  `把 input_json 里 {"text": "..."} 的 text 字段反转后返回`,
		InputSchema:  `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
		OutputSchema: `{"type":"object","properties":{"result":{"type":"string"}}}`,
	},
}

// externalSpec is the JSON shape returned by GET {baseURL}/spec.
type externalSpec struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  string `json:"input_schema"`
	OutputSchema string `json:"output_schema"`
}

// externalInvokeRequest is the JSON body sent to POST {baseURL}/invoke.
type externalInvokeRequest struct {
	InputJSON string `json:"input_json"`
}

// externalInvokeReply is the JSON shape returned by POST {baseURL}/invoke.
type externalInvokeReply struct {
	Ok         bool   `json:"ok"`
	OutputJSON string `json:"output_json"`
	Error      string `json:"error"`
}

func (s *ToolServiceImpl) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsReply, error) {
	result := make([]*toolv1.ToolSpec, len(builtinTools))
	copy(result, builtinTools)
	specs, _ := s.discoverExternal()
	result = append(result, specs...)
	return &toolv1.ListToolsReply{Tools: result}, nil
}

// discoverExternal 拉取所有外部端点的 /spec，刷新内部 name->URL 映射，
// 返回发现的 spec 列表与映射。ListTools 与 Invoke(未命中时) 共用，
// 确保即使先 Invoke 未 ListTools 也能解析到外部工具。
func (s *ToolServiceImpl) discoverExternal() ([]*toolv1.ToolSpec, map[string]string) {
	specs := make([]*toolv1.ToolSpec, 0)
	urls := make(map[string]string)
	client := &http.Client{Timeout: 3 * time.Second}
	for _, baseURL := range s.conf.ExternalTools {
		spec, err := fetchSpec(client, baseURL)
		if err != nil {
			log.Printf("WARNING: 外部工具端点 %s 不可达: %v", baseURL, err)
			continue
		}
		specs = append(specs, &toolv1.ToolSpec{
			Name:         spec.Name,
			Description:  spec.Description,
			InputSchema:  spec.InputSchema,
			OutputSchema: spec.OutputSchema,
		})
		urls[spec.Name] = baseURL
	}
	s.mu.Lock()
	s.externalURLs = urls
	s.mu.Unlock()
	return specs, urls
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
		s.mu.RLock()
		baseURL, found := s.externalURLs[req.Name]
		s.mu.RUnlock()

		if !found {
			// 映射可能尚未建立（Invoke 未必先于 ListTools 调用）——按需刷新一次再查。
			_, urls := s.discoverExternal()
			baseURL, found = urls[req.Name]
		}
		if !found {
			return nil, status.Errorf(codes.NotFound, "tool %q not found", req.Name)
		}

		reply, err := invokeExternal(baseURL, req.InputJson)
		if err != nil {
			return &toolv1.InvokeReply{Ok: false, Error: fmt.Sprintf("external invoke error: %v", err)}, nil
		}
		return &toolv1.InvokeReply{
			Ok:         reply.Ok,
			OutputJson: reply.OutputJSON,
			Error:      reply.Error,
		}, nil
	}
}

// fetchSpec calls GET {baseURL}/spec and decodes the response.
func fetchSpec(client *http.Client, baseURL string) (*externalSpec, error) {
	resp, err := client.Get(baseURL + "/spec")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s/spec", resp.StatusCode, baseURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var spec externalSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, fmt.Errorf("parse spec JSON: %w", err)
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("spec missing 'name' field")
	}
	return &spec, nil
}

// invokeExternal calls POST {baseURL}/invoke with the given inputJSON payload.
func invokeExternal(baseURL, inputJSON string) (*externalInvokeReply, error) {
	reqBody, err := json.Marshal(externalInvokeRequest{InputJSON: inputJSON})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(baseURL+"/invoke", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var reply externalInvokeReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("parse invoke JSON: %w", err)
	}
	return &reply, nil
}
