package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func connection(t *testing.T) (*Client, net.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	local, remote := net.Pipe()
	t.Cleanup(func() { cancel(); _ = local.Close(); _ = remote.Close() })
	return NewClient(ctx, local, local), remote, ctx
}

func TestRepliesMatchConcurrentCalls(t *testing.T) {
	client, server, ctx := connection(t)
	type outcome struct {
		method, value string
		err           error
	}
	outcomes := make(chan outcome, 2)
	for _, method := range []string{"first", "second"} {
		go func() {
			var result struct {
				Value string `json:"value"`
			}
			err := client.Call(ctx, method, nil, &result)
			outcomes <- outcome{method: method, value: result.Value, err: err}
		}()
	}
	decoder := json.NewDecoder(server)
	var requests [2]struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	for i := range requests {
		if err := decoder.Decode(&requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	encoder := json.NewEncoder(server)
	// Replies may arrive in the reverse order while notifications interleave.
	if err := encoder.Encode(
		map[string]any{
			"method": "thread/realtime/started",
			"params": map[string]any{},
		},
	); err != nil {
		t.Fatal(err)
	}
	for i := 1; i >= 0; i-- {
		if err := encoder.Encode(
			map[string]any{
				"id":     requests[i].ID,
				"result": map[string]string{"value": requests[i].Method},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		result := <-outcomes
		if result.err != nil || result.value != result.method {
			t.Fatalf("mismatched reply: %+v", result)
		}
	}
}

func TestCanceledRequestDoesNotBlockLaterReplies(t *testing.T) {
	client, server, ctx := connection(t)
	callCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- client.Call(callCtx, "first", nil, nil) }()
	decoder := json.NewDecoder(server)
	var first struct {
		ID int `json:"id"`
	}
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if err := json.NewEncoder(server).
		Encode(map[string]any{"id": first.ID, "result": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	go func() { result <- client.Call(ctx, "second", nil, nil) }()
	var second struct {
		ID int `json:"id"`
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(server).
		Encode(map[string]any{"id": second.ID, "result": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestPendingCallFailsWhenServerExits(t *testing.T) {
	client, server, ctx := connection(t)
	result := make(chan error, 1)
	go func() { result <- client.Call(ctx, "account/read", nil, nil) }()
	var request map[string]any
	if err := json.NewDecoder(server).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
}

func TestApprovalRequestsAreDeclined(t *testing.T) {
	_, server, _ := connection(t)
	encoder, decoder := json.NewEncoder(server), json.NewDecoder(server)
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval"} {
		if err := encoder.Encode(
			map[string]any{
				"id":     "approval-1",
				"method": method,
				"params": map[string]any{},
			},
		); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			ID     string `json:"id"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
		}
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != "approval-1" || reply.Result.Decision != "decline" {
			t.Fatalf("unexpected approval: %+v", reply)
		}
	}
}
