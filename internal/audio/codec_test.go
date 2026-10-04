package audio

import (
	"math"
	"testing"
)

func TestOpusCarriesTwentyMillisecondSpeechFrames(t *testing.T) {
	c, err := newCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	pcm := make([]int16, frameSamples)
	for i := range pcm {
		pcm[i] = int16(12000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
	}
	packet, err := c.encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := c.decode(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != frameSamples {
		t.Fatalf("got %d samples, want %d", len(decoded), frameSamples)
	}
	peak := int16(0)
	for _, sample := range decoded {
		if sample > peak {
			peak = sample
		}
	}
	if peak < 100 {
		t.Fatal("encoded speech was decoded as silence")
	}
}
