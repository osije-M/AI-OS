// Command evalctl 是 AI-OS 的离线评测 CLI：拿一份基准集端到端打 gateway，
// 量化路由准确率 / 成功率 / 平均延迟，结果落独立 eval.db（可对比历史 run）。
//
// 用法：
//
//	evalctl -base http://127.0.0.1:8000 -suite eval/suite.yaml -db eval.db
//	evalctl -api-key <key>      # 当 gateway 鉴权开启时
//	evalctl -strict             # 有未通过用例时以非 0 退出（便于 CI 门禁）
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"
)

func main() {
	base := flag.String("base", envOr("GATEWAY_BASE", "http://127.0.0.1:8000"), "gateway base URL")
	suitePath := flag.String("suite", "eval/suite.yaml", "benchmark suite YAML")
	dbPath := flag.String("db", "eval.db", "eval result SQLite db path")
	apiKey := flag.String("api-key", os.Getenv("EVAL_API_KEY"), "API key (gateway 鉴权开启时用)")
	timeout := flag.Duration("timeout", 180*time.Second, "单次请求超时")
	strict := flag.Bool("strict", false, "有未通过用例时以退出码 1 结束")
	flag.Parse()

	tasks, err := LoadSuite(*suitePath)
	if err != nil {
		fatal("load suite %q: %v", *suitePath, err)
	}
	if len(tasks) == 0 {
		fatal("suite %q 为空", *suitePath)
	}

	store, err := OpenStore(*dbPath)
	if err != nil {
		fatal("open store %q: %v", *dbPath, err)
	}
	defer store.Close()

	client := &http.Client{Timeout: *timeout}
	runID := fmt.Sprintf("eval-%d", time.Now().UnixNano())
	started := time.Now()

	fmt.Printf("running %d task(s) against %s\n\n", len(tasks), *base)
	results := make([]Result, 0, len(tasks))
	for i, t := range tasks {
		r := runTask(client, *base, *apiKey, t)
		results = append(results, r)
		printProgress(i+1, len(tasks), r)
	}
	ended := time.Now()

	sum := computeSummary(results)
	printReport(results, sum)

	meta := RunMeta{
		RunID: runID, Suite: *suitePath, GitSHA: gitSHA(),
		StartedAt: started.UnixMilli(), EndedAt: ended.UnixMilli(),
	}
	if err := store.SaveRun(meta, sum, results); err != nil {
		fatal("save run: %v", err)
	}
	fmt.Printf("\nsaved run %s -> %s\n", runID, *dbPath)

	if *strict && sum.Passed < sum.Total {
		os.Exit(1)
	}
}

func printProgress(i, n int, r Result) {
	tag := "ok"
	switch {
	case r.Err != "":
		tag = "ERR " + r.Err
	case !r.statusMatched() || (r.ExpectRoute != "" && r.ActualRoute != r.ExpectRoute):
		tag = "MISS"
	}
	fmt.Printf("[%d/%d] %-18s route=%-8s status=%-7s %5dms  %s\n",
		i, n, r.TaskID, dash(r.ActualRoute), dash(r.ActualStatus), r.LatencyMs, tag)
}

func printReport(results []Result, s Summary) {
	fmt.Println("\n" + strings.Repeat("-", 72))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tROUTE(exp→act)\tSTATUS(exp→act)\tLAT(ms)\tRESULT")
	for _, r := range results {
		result := "PASS"
		if r.Err != "" {
			result = "ERROR"
		} else if !r.statusMatched() || (r.ExpectRoute != "" && r.ActualRoute != r.ExpectRoute) {
			result = "MISS"
		}
		fmt.Fprintf(w, "%s\t%s→%s\t%s→%s\t%d\t%s\n",
			r.TaskID, dash(r.ExpectRoute), dash(r.ActualRoute),
			dash(r.ExpectStatus), dash(r.ActualStatus), r.LatencyMs, result)
	}
	w.Flush()
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("total=%d  passed=%d  route_acc=%.0f%% (%d/%d)  success_rate=%.0f%%  avg_latency=%.0fms\n",
		s.Total, s.Passed, s.RouteAcc*100, s.RouteMatched, s.RouteExpected,
		s.SuccessRate*100, s.AvgLatencyMs)
}

func gitSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "evalctl: "+format+"\n", a...)
	os.Exit(2)
}
