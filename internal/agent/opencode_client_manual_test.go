package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
)

func TestOpenCodeClient_Manual(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := NewOpenCodeClient("http://127.0.0.1:4096", "", logger)
	ctx := context.Background()

	err := client.HealthCheck(ctx)
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	fmt.Println("Health check passed.")

	session, err := client.CreateSession(ctx, "Test Session", "", nil)
	if err != nil {
		t.Fatalf("Create session failed: %v", err)
	}
	fmt.Printf("Created session: %s\n", session.ID)

	resp, err := client.SendMessage(ctx, session.ID, "echo 'Testing command execution'", nil)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	fmt.Printf("Message executed successfully, text response length: %d\n", len(resp.Info.Content))
}
