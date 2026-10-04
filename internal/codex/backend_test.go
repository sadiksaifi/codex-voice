package codex

import (
	"encoding/json"
	"testing"
)

func TestVoiceDisablesInheritedCapabilities(t *testing.T) {
	client, server, ctx := connection(t)
	backend := NewBackend(client, "/tmp")
	finished := make(chan error, 1)
	go func() {
		_, _, err := backend.Start(ctx, "offer", "juniper")
		finished <- err
	}()
	decoder := json.NewDecoder(server)
	encoder := json.NewEncoder(server)
	for {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			t.Fatal(err)
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialized":
			continue
		case "account/read":
			result = map[string]any{"account": map[string]string{
				"type": "chatgpt", "planType": "pro",
			}}
		case "config/read":
			result = map[string]any{"config": map[string]any{
				"mcp_servers": map[string]any{
					"server.with.dots": map[string]bool{"enabled": true},
				},
				"plugins": map[string]any{
					"demo@example": map[string]bool{"enabled": true},
				},
				"apps": map[string]any{
					"_default":           map[string]bool{"enabled": false},
					"explicitly-enabled": map[string]bool{"enabled": true},
				},
			}}
		case "thread/start":
			var params struct {
				Config map[string]json.RawMessage `json:"config"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			for category, name := range map[string]string{
				"mcp_servers": "server.with.dots",
				"plugins":     "demo@example",
				"apps":        "explicitly-enabled",
			} {
				var entries map[string]struct {
					Enabled *bool `json:"enabled"`
				}
				if err := json.Unmarshal(
					params.Config[category],
					&entries,
				); err != nil {
					t.Fatal(err)
				}
				if enabled := entries[name].Enabled; enabled == nil ||
					*enabled {
					t.Fatalf("inherited %s %q was not disabled", category, name)
				}
			}
			result = map[string]any{"thread": map[string]string{"id": "voice"}}
		case "thread/realtime/start":
			if err := encoder.Encode(map[string]any{
				"id": request.ID, "result": result,
			}); err != nil {
				t.Fatal(err)
			}
			if err := encoder.Encode(map[string]any{
				"method": "thread/realtime/sdp",
				"params": map[string]string{"sdp": "answer"},
			}); err != nil {
				t.Fatal(err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := encoder.Encode(map[string]any{
			"id": request.ID, "result": result,
		}); err != nil {
			t.Fatal(err)
		}
	}
}
