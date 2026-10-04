package codex

import (
	"encoding/json"
	"fmt"

	"github.com/sadiksaifi/voice/internal/voice"
)

// Decode normalizes GPT-Live data-channel events for the application.
func (b *Backend) Decode(data []byte) []voice.Event {
	var event struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Item  struct {
			Text string `json:"text"`
		} `json:"item"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &event) != nil {
		return nil
	}
	switch event.Type {
	case "session.started":
		return []voice.Event{{Kind: "connected"}}
	case "session.input_transcript.delta", "session.output_transcript.delta":
		role := "You"
		if event.Type == "session.output_transcript.delta" {
			role = "Assistant"
		}
		return []voice.Event{
			{Kind: "transcript", Role: role, Text: event.Delta},
		}
	case "input_transcript.added", "output_transcript.added":
		role := "You"
		if event.Type == "output_transcript.added" {
			role = "Assistant"
		}
		return []voice.Event{
			{Kind: "transcript", Role: role, Text: event.Item.Text},
		}
	case "input_audio_buffer.speech_started",
		"session.input_audio.speech_started":
		return []voice.Event{{Kind: "interrupt"}}
	case "error":
		return []voice.Event{
			{Kind: "error", Err: fmt.Errorf("voice: %s", event.Error.Message)},
		}
	default:
		return nil
	}
}
