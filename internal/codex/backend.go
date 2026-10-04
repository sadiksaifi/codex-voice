package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sadiksaifi/voice/internal/voice"
)

const voicePrompt = "You are a voice conversation partner. " +
	"Listen and reply naturally in short, plain sentences. " +
	"Answer directly without delegating or using tools."

type Backend struct {
	client    *Client
	directory string
	mu        sync.RWMutex
	threadID  string
	answer    chan response
	events    chan voice.Event
}

func NewBackend(client *Client, directory string) *Backend {
	return &Backend{
		client:    client,
		directory: directory,
		answer:    make(chan response, 1),
		events:    make(chan voice.Event, 64),
	}
}

func (b *Backend) Events() <-chan voice.Event { return b.events }

func (b *Backend) Start(
	ctx context.Context,
	offer, selectedVoice string,
) (string, string, error) {
	go b.watch(ctx)
	if err := b.client.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "codex_voice",
			"title":   "Codex voice",
			"version": "0.1.0",
		},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		return "", "", err
	}
	if err := b.client.notify("initialized"); err != nil {
		return "", "", err
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
			Plan string `json:"planType"`
		} `json:"account"`
	}
	if err := b.client.Call(
		ctx,
		"account/read",
		map[string]bool{"refreshToken": false},
		&account,
	); err != nil {
		return "", "", err
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return "", "", fmt.Errorf(
			"ChatGPT login required; run codex login and choose Sign in with ChatGPT",
		)
	}
	configuration, err := b.conversationConfig(ctx)
	if err != nil {
		return "", "", err
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := b.client.Call(ctx, "thread/start", map[string]any{
		"cwd":                     b.directory,
		"ephemeral":               true,
		"approvalPolicy":          "on-request",
		"sandbox":                 "read-only",
		"baseInstructions":        voicePrompt,
		"developerInstructions":   "",
		"environments":            []any{},
		"dynamicTools":            []any{},
		"selectedCapabilityRoots": []any{},
		"config":                  configuration,
	}, &thread); err != nil {
		return "", "", err
	}
	b.mu.Lock()
	b.threadID = thread.Thread.ID
	b.mu.Unlock()
	if err := b.client.Call(ctx, "thread/realtime/start", map[string]any{
		"threadId":              thread.Thread.ID,
		"outputModality":        "audio",
		"version":               "v3",
		"voice":                 selectedVoice,
		"prompt":                voicePrompt,
		"includeStartupContext": false,
		"transport": map[string]string{
			"type": "webrtc",
			"sdp":  offer,
		},
	}, nil); err != nil {
		return "", "", err
	}
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	select {
	case answer := <-b.answer:
		return string(answer.Result), account.Account.Plan, answer.Err
	case <-timer.C:
		return "", "", fmt.Errorf("timed out starting Codex voice")
	case <-b.client.done:
		return "", "", fmt.Errorf("codex exited during voice startup")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

func (b *Backend) conversationConfig(
	ctx context.Context,
) (map[string]any, error) {
	var current struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := b.client.Call(ctx, "config/read", map[string]any{
		"cwd": b.directory, "includeLayers": false,
	}, &current); err != nil {
		return nil, fmt.Errorf("read Codex capabilities: %w", err)
	}
	features := map[string]bool{"skip_host_skill_discovery": true}
	for _, name := range []string{
		"shell_tool", "unified_exec", "shell_snapshot", "deferred_executor",
		"code_mode", "code_mode_host", "code_mode_only", "search_tool",
		"plugins", "plugin_hooks", "recommended_plugins", "tool_suggest",
		"apps", "psp", "enable_mcp_apps", "multi_agent", "enable_fanout",
		"skill_search", "skill_mcp_dependency_install",
		"skill_env_var_dependency_prompt", "memories", "chronicle", "hooks",
		"browser_use", "computer_use", "image_generation", "artifact",
		"goals", "context_management", "request_permissions_tool",
		"send_async_message", "send_message_to_user_async",
	} {
		features[name] = false
	}
	configuration := map[string]any{
		"experimental_realtime_ws_backend_prompt": voicePrompt,
		"project_doc_max_bytes":                   0,
		"include_apps_instructions":               false,
		"include_collaboration_mode_instructions": false,
		"include_environment_context":             false,
		"web_search":                              "disabled",
		"agents": map[string]bool{
			"enabled": false,
		},
		"features": features,
		"skills": map[string]bool{
			"include_instructions": false,
		},
		"cloud": map[string]any{
			"skills": map[string]bool{"enabled": false},
		},
		"orchestrator": map[string]any{
			"mcp": map[string]bool{"enabled": false},
		},
		"tools": map[string]any{
			"update_plan": map[string]bool{
				"enabled": false,
			},
			"experimental_request_user_input": map[string]bool{
				"enabled": false,
			},
		},
	}
	// Disable each configured entry: a default alone cannot override explicit enables.
	for _, category := range []string{"mcp_servers", "plugins", "apps"} {
		var entries map[string]json.RawMessage
		if data := current.Config[category]; data != nil {
			if err := json.Unmarshal(data, &entries); err != nil {
				return nil, fmt.Errorf("read Codex %s: %w", category, err)
			}
		}
		disabled := map[string]any{}
		for name := range entries {
			disabled[name] = map[string]bool{"enabled": false}
		}
		if category == "apps" {
			disabled["_default"] = map[string]bool{"enabled": false}
		}
		configuration[category] = disabled
	}
	return configuration, nil
}

func (b *Backend) SendText(ctx context.Context, text string) error {
	b.mu.RLock()
	id := b.threadID
	b.mu.RUnlock()
	return b.client.Call(
		ctx,
		"thread/realtime/appendText",
		map[string]string{"threadId": id, "text": text},
		nil,
	)
}

func (b *Backend) Stop(ctx context.Context) error {
	b.mu.RLock()
	id := b.threadID
	b.mu.RUnlock()
	if id == "" {
		return nil
	}
	return b.client.Call(
		ctx,
		"thread/realtime/stop",
		map[string]string{"threadId": id},
		nil,
	)
}

func (b *Backend) watch(ctx context.Context) {
	defer close(b.events)
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-b.client.events:
			if !ok {
				return
			}
			var params struct {
				SDP     string `json:"sdp"`
				Message string `json:"message"`
				Delta   string `json:"delta"`
			}
			if json.Unmarshal(message.Params, &params) != nil {
				continue
			}
			var event voice.Event
			switch message.Method {
			case "thread/realtime/sdp":
				select {
				case b.answer <- response{Result: json.RawMessage(params.SDP)}:
				default:
				}
				continue
			case "thread/realtime/error":
				event = voice.Event{
					Kind: "error",
					Err:  fmt.Errorf("codex voice: %s", params.Message),
				}
				select {
				case b.answer <- response{Err: event.Err}:
				default:
				}
			case "item/agentMessage/delta":
				event = voice.Event{
					Kind: "transcript",
					Role: "Codex",
					Text: params.Delta,
				}
			case "thread/realtime/closed":
				event = voice.Event{
					Kind: "error",
					Err:  fmt.Errorf("codex voice session ended"),
				}
			default:
				continue
			}
			select {
			case b.events <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}
