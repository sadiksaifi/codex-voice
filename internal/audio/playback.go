package audio

import "encoding/binary"

// Keep 100 ms of audio ahead of the speaker to absorb uneven packet arrivals.
const playbackPrebuffer = sampleRate / 10

type playbackBuffer struct {
	samples [sampleRate]int16
	head    int
	size    int
	waited  int
	playing bool
}

func (b *playbackBuffer) push(pcm []int16) {
	// Bound latency to one second by dropping only the excess oldest samples.
	discard := min(max(b.size+len(pcm)-len(b.samples), 0), b.size)
	b.head = (b.head + discard) % len(b.samples)
	b.size -= discard
	if len(pcm) > len(b.samples) {
		pcm = pcm[len(pcm)-len(b.samples):]
	}
	tail := (b.head + b.size) % len(b.samples)
	n := copy(b.samples[tail:], pcm)
	copy(b.samples[:], pcm[n:])
	b.size += len(pcm)
}

// read runs on the speaker clock. The caller supplies a cleared output buffer.
func (b *playbackBuffer) read(output []byte) {
	if b.size == 0 {
		return
	}
	if !b.playing {
		b.waited += len(output) / 2
		// Release short utterances even if no more packets arrive.
		if b.size < playbackPrebuffer && b.waited < playbackPrebuffer {
			return
		}
		b.playing = true
	}
	n := min(len(output)/2, b.size)
	for i := range n {
		sample := b.samples[(b.head+i)%len(b.samples)]
		binary.LittleEndian.PutUint16(output[i*2:], uint16(sample))
	}
	b.head = (b.head + n) % len(b.samples)
	b.size -= n
	if b.size == 0 {
		b.clear()
	}
}

func (b *playbackBuffer) clear() {
	b.head, b.size, b.waited = 0, 0, 0
	b.playing = false
}
