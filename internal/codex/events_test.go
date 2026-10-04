package codex

import "testing"

func TestLiveTranscriptsKeepSpeakerAndText(t *testing.T) {
	backend := &Backend{}
	for _, sample := range []struct{ data, role, text string }{
		{`{"type":"session.input_transcript.delta","delta":" Hello"}`, "You", " Hello"},
		{`{"type":"session.output_transcript.delta","delta":" there"}`, "Assistant", " there"},
		{`{"type":"output_transcript.added","item":{"type":"output_transcript","text":" you hear"},"start_ms":1200,"end_ms":1400}`, "Assistant", " you hear"},
		{`{"type":"input_transcript.added","item":{"type":"input_transcript","text":"Yes, I can."}}`, "You", "Yes, I can."},
	} {
		events := backend.Decode([]byte(sample.data))
		if len(events) != 1 || events[0].Kind != "transcript" ||
			events[0].Role != sample.role ||
			events[0].Text != sample.text {
			t.Fatalf("unexpected transcript: %+v", events)
		}
	}
}
