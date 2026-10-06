package audio

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"
)

// The callback is the hardware boundary. Drive it without opening a speaker.
func playbackDevice(t *testing.T) (*Device, [][]byte, []int16) {
	t.Helper()
	encoder, err := newCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.close()
	decoder, err := newCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.close()
	deviceCodec, err := newCodec()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deviceCodec.close)
	d := &Device{codec: deviceCodec, input: make(chan []int16, 1)}
	var packets [][]byte
	var expected []int16
	for frame := range 70 {
		pcm := make([]int16, 960)
		for i := range pcm {
			pcm[i] = int16(
				12000 * math.Sin(2*math.Pi*440*float64(frame*960+i)/48000),
			)
		}
		packet, encodeErr := encoder.encode(pcm)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		decoded, decodeErr := decoder.decode(packet)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		packets = append(packets, packet)
		expected = append(expected, decoded...)
	}
	return d, packets, expected
}

func queuePackets(t *testing.T, d *Device, packets [][]byte) {
	t.Helper()
	for _, packet := range packets {
		if _, err := d.Play(packet); err != nil {
			t.Fatal(err)
		}
	}
}

func speakerSamples(d *Device, count int) []int16 {
	output := make([]byte, count*2)
	d.callback(output, nil, uint32(count))
	pcm := make([]int16, count)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(output[i*2:]))
	}
	return pcm
}

func TestPlaybackSmoothsUnevenPacketArrival(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:1])
	if got := speakerSamples(d, 960); !slices.Equal(got, make([]int16, 960)) {
		t.Fatal("playback started before buffering network jitter")
	}
	queuePackets(t, d, packets[1:5])
	var got []int16
	// Three speaker periods pass without any new network packets.
	for range 3 {
		got = append(got, speakerSamples(d, 960)...)
	}
	queuePackets(t, d, packets[5:8])
	for range 5 {
		got = append(got, speakerSamples(d, 960)...)
	}
	if !slices.Equal(got, expected[:8*960]) {
		t.Fatal("uneven arrivals inserted silence or changed sample order")
	}
}

func TestPlaybackRebuffersAfterUnderrun(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:5])
	_ = speakerSamples(d, 6*960)
	queuePackets(t, d, packets[5:6])
	if got := speakerSamples(d, 960); !slices.Equal(got, make([]int16, 960)) {
		t.Fatal("playback resumed without rebuilding the jitter cushion")
	}
	queuePackets(t, d, packets[6:10])
	if got := speakerSamples(
		d,
		5*960,
	); !slices.Equal(
		got,
		expected[5*960:10*960],
	) {
		t.Fatal("rebuffering lost samples")
	}
}

func TestPlaybackDrainsShortAudio(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:1])
	for range 6 {
		got := speakerSamples(d, 960)
		if slices.Equal(got, expected[:960]) {
			return
		}
		if !slices.Equal(got, make([]int16, 960)) {
			t.Fatal("short audio changed while buffering")
		}
	}
	t.Fatal("short audio remained buffered beyond 100 milliseconds")
}

func TestPlaybackOverflowKeepsNewestSecond(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:51])
	if got := speakerSamples(
		d,
		48000,
	); !slices.Equal(
		got,
		expected[960:51*960],
	) {
		t.Fatal("overflow discarded more audio than needed to bound latency")
	}
}

func TestPlaybackPreservesSamplesAcrossVariableCallbacks(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:30])
	got := speakerSamples(d, 17*960+480)
	// Wrap the queue while a half-frame remains at its head.
	queuePackets(t, d, packets[30:60])
	remaining := 60*960 - len(got)
	for remaining > 0 {
		count := min(1440, remaining)
		got = append(got, speakerSamples(d, count)...)
		remaining -= count
	}
	if !slices.Equal(got, expected[:60*960]) {
		t.Fatal("variable speaker callbacks lost or reordered samples")
	}
}

func TestClearPlaybackDiscardsAudioAndRebuffers(t *testing.T) {
	d, packets, expected := playbackDevice(t)
	queuePackets(t, d, packets[:5])
	_ = speakerSamples(d, 960)
	d.ClearPlayback()
	if got := speakerSamples(d, 960); !slices.Equal(got, make([]int16, 960)) {
		t.Fatal("interrupted audio reached the speaker")
	}
	queuePackets(t, d, packets[5:6])
	if got := speakerSamples(d, 960); !slices.Equal(got, make([]int16, 960)) {
		t.Fatal("interruption did not reset buffering")
	}
	queuePackets(t, d, packets[6:10])
	if got := speakerSamples(
		d,
		5*960,
	); !slices.Equal(
		got,
		expected[5*960:10*960],
	) {
		t.Fatal("new speech contains interrupted audio")
	}
}
