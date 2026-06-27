package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// traceData uses json.Number for int64 fields because protojson serializes int64 as
// JSON strings (e.g. "5678") to avoid JavaScript precision loss.
type traceData struct {
	TraceId   string            `json:"traceId"`
	Task      string            `json:"task"`
	Status    string            `json:"status"`
	Route     string            `json:"route"`
	ElapsedMs json.Number       `json:"elapsedMs"`
	StartedAt json.Number       `json:"startedAt"`
	EndedAt   json.Number       `json:"endedAt"`
	Output    string            `json:"output"`
	Steps     []map[string]any  `json:"steps"`
}

// numToInt64 converts a json.Number or string to int64.
func numToInt64(v any) int64 {
	switch vv := v.(type) {
	case json.Number:
		n, _ := vv.Int64()
		return n
	case float64:
		return int64(vv)
	case string:
		var n int64
		fmt.Sscanf(vv, "%d", &n)
		return n
	}
	return 0
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tracectl <trace_id>")
		os.Exit(1)
	}
	traceID := os.Args[1]
	base := os.Getenv("GATEWAY_BASE")
	if base == "" {
		base = "http://127.0.0.1:8000"
	}
	url := base + "/v1/trace/" + traceID
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "request error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		fmt.Fprintf(os.Stderr, "trace %s not found\n", traceID)
		os.Exit(1)
	}
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "error %d: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var t traceData
	if err := json.Unmarshal(body, &t); err != nil {
		fmt.Fprintf(os.Stderr, "parse error: %v\n", err)
		os.Exit(1)
	}

	// Styles
	bold := lipgloss.NewStyle().Bold(true)
	okBadge := lipgloss.NewStyle().Background(lipgloss.Color("2")).Foreground(lipgloss.Color("15")).Padding(0, 1)
	failBadge := lipgloss.NewStyle().Background(lipgloss.Color("1")).Foreground(lipgloss.Color("15")).Padding(0, 1)
	routeBadge := lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15")).Padding(0, 1)
	controlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))   // cyan
	llmStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("5"))       // magenta
	toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))      // yellow
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))     // red
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))       // gray
	highlightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true) // bright yellow

	// Header
	statusBadge := okBadge.Render("OK")
	if t.Status == "FAILED" {
		statusBadge = failBadge.Render("FAILED")
	}
	fmt.Println()
	fmt.Printf("  %s  %s  %s  %s\n",
		bold.Render(t.Task),
		statusBadge,
		routeBadge.Render(t.Route),
		dimStyle.Render(fmt.Sprintf("%dms", numToInt64(t.ElapsedMs))),
	)
	fmt.Printf("  %s %s\n", dimStyle.Render("trace:"), dimStyle.Render(t.TraceId))
	fmt.Println()

	// Timeline
	fmt.Println("  " + bold.Render("Timeline"))
	for _, step := range t.Steps {
		seq := numToInt64(step["seq"])
		node := fmt.Sprintf("%v", step["node"])
		typ := fmt.Sprintf("%v", step["type"])
		summary := fmt.Sprintf("%v", step["summary"])
		latencyMs := numToInt64(step["latencyMs"])
		stepStatus := fmt.Sprintf("%v", step["status"])

		var typeStyle lipgloss.Style
		switch typ {
		case "control":
			typeStyle = controlStyle
		case "llm":
			typeStyle = llmStyle
		case "tool":
			typeStyle = toolStyle
		default:
			typeStyle = dimStyle
		}

		// highlight retry/switch nodes
		nodeStr := typeStyle.Render(node)
		if strings.Contains(node, "retry") || strings.Contains(node, "switch") {
			nodeStr = highlightStyle.Render(node)
		}

		statusMark := "v"
		summaryStr := summary
		if stepStatus == "error" {
			statusMark = "x"
			summaryStr = errorStyle.Render(summary)
		}

		line := fmt.Sprintf("  %s %s  %s  %s  %s",
			dimStyle.Render(fmt.Sprintf("%02d", seq)),
			dimStyle.Render(statusMark),
			nodeStr,
			summaryStr,
			dimStyle.Render(fmt.Sprintf("%dms", latencyMs)),
		)
		fmt.Println(line)
	}

	// Output
	if t.Output != "" {
		fmt.Println()
		fmt.Println("  " + bold.Render("Output"))
		outputPreview := t.Output
		if len(outputPreview) > 500 {
			outputPreview = outputPreview[:500] + "...(truncated)"
		}
		for _, line := range strings.Split(outputPreview, "\n") {
			fmt.Println("  " + line)
		}
	}
	fmt.Println()
}
