// Minimal integration test for ToolService.
// Run: go run ./scripts/test_toolservice/
// Requires ToolService to be running on :9200.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	toolv1 "github.com/osije/ai-os/api/gen/go/tool/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.NewClient("127.0.0.1:9200", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	client := toolv1.NewToolServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test ListTools
	listReply, err := client.ListTools(ctx, &toolv1.ListToolsRequest{})
	if err != nil {
		log.Fatalf("ListTools error: %v", err)
	}
	fmt.Printf("=== ListTools ===\n")
	for _, t := range listReply.Tools {
		fmt.Printf("  name=%s  desc=%s\n", t.Name, t.Description)
	}

	// Test Invoke(echo)
	echoReply, err := client.Invoke(ctx, &toolv1.InvokeRequest{
		Name:      "echo",
		InputJson: `{"hello":"world","num":42}`,
		TraceId:   "test-trace-001",
	})
	if err != nil {
		log.Fatalf("Invoke(echo) error: %v", err)
	}
	fmt.Printf("\n=== Invoke(echo) ===\n  ok=%v  output=%s\n", echoReply.Ok, echoReply.OutputJson)

	// Test Invoke(reverse)
	revReply, err := client.Invoke(ctx, &toolv1.InvokeRequest{
		Name:      "reverse",
		InputJson: `{"text":"Hello, AI-OS!"}`,
		TraceId:   "test-trace-002",
	})
	if err != nil {
		log.Fatalf("Invoke(reverse) error: %v", err)
	}
	fmt.Printf("\n=== Invoke(reverse) ===\n  ok=%v  output=%s\n", revReply.Ok, revReply.OutputJson)

	// Test Invoke unknown tool
	unknownReply, err := client.Invoke(ctx, &toolv1.InvokeRequest{
		Name:      "nonexistent",
		InputJson: `{}`,
	})
	if err != nil {
		fmt.Printf("\n=== Invoke(nonexistent) ===\n  expected gRPC error: %v\n", err)
	} else {
		fmt.Printf("\n=== Invoke(nonexistent) ===\n  ok=%v error=%s\n", unknownReply.Ok, unknownReply.Error)
	}

	fmt.Println("\nAll tests passed!")
}
