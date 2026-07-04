package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result 是一条用例的评测结果。
type Result struct {
	TaskID       string
	Task         string
	ExpectRoute  string
	ActualRoute  string
	ExpectStatus string
	ActualStatus string
	LatencyMs    int64
	TraceID      string
	Err          string
}

// statusMatched 报告实际状态是否符合期望（期望为空视为不校验、算通过）。
func (r Result) statusMatched() bool {
	return r.ExpectStatus == "" || r.ActualStatus == r.ExpectStatus
}

type runReply struct {
	TraceID string `json:"traceId"`
	Output  string `json:"output"`
	Status  string `json:"status"`
}

type traceReply struct {
	Route  string `json:"route"`
	Status string `json:"status"`
}

// runTask 端到端跑一条用例：POST /v1/run 拿状态,再 GET /v1/trace/{id} 取路由。
func runTask(client *http.Client, base, apiKey string, t Task) Result {
	r := Result{TaskID: t.ID, Task: t.Task, ExpectRoute: t.ExpectRoute, ExpectStatus: t.ExpectStatus}

	body, _ := json.Marshal(map[string]string{"task": t.Task})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, err := client.Do(req)
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		r.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b))
		return r
	}
	var rr runReply
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		r.Err = "decode run reply: " + err.Error()
		return r
	}
	r.TraceID = rr.TraceID
	r.ActualStatus = rr.Status
	r.ActualRoute = fetchRoute(client, base, apiKey, rr.TraceID)
	return r
}

// fetchRoute 从 trace 取路由（/v1/run 响应不含 route,需查 trace）。
// orchestrator 的 trace 落库是异步最佳努力的,故这里有界轮询:等 trace 出现再读,
// 避免"发完即查"的竞态导致 route 恒空。
func fetchRoute(client *http.Client, base, apiKey, traceID string) string {
	if traceID == "" {
		return ""
	}
	const attempts = 15
	for i := 0; i < attempts; i++ {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/trace/"+traceID, nil)
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err == nil {
			found := resp.StatusCode == http.StatusOK
			var tr traceReply
			if found {
				_ = json.NewDecoder(resp.Body).Decode(&tr)
			}
			resp.Body.Close()
			if found {
				return tr.Route // trace 已落库(route 可能为空,如 deny)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}
