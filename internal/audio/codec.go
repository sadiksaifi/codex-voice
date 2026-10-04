package audio

/*
#cgo pkg-config: opus
#include <opus.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

const (
	sampleRate   = 48000
	frameSamples = 960
)

func freeDeviceID(pointer unsafe.Pointer) { C.free(pointer) }

type codec struct {
	encoder *C.OpusEncoder
	decoder *C.OpusDecoder
}

func newCodec() (*codec, error) {
	var code C.int
	encoder := C.opus_encoder_create(
		sampleRate,
		1,
		C.OPUS_APPLICATION_VOIP,
		&code,
	)
	if code != C.OPUS_OK {
		return nil, opusError(code)
	}
	decoder := C.opus_decoder_create(sampleRate, 1, &code)
	if code != C.OPUS_OK {
		C.opus_encoder_destroy(encoder)
		return nil, opusError(code)
	}
	return &codec{encoder: encoder, decoder: decoder}, nil
}

func opusError(code C.int) error {
	return fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(code)))
}

func (c *codec) encode(pcm []int16) ([]byte, error) {
	packet := make([]byte, 4000)
	n := C.opus_encode(c.encoder,
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])), frameSamples,
		(*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)))
	if n < 0 {
		return nil, opusError(n)
	}
	return packet[:int(n)], nil
}

func (c *codec) decode(packet []byte) ([]int16, error) {
	if len(packet) == 0 {
		return nil, fmt.Errorf("empty Opus packet")
	}
	pcm := make([]int16, 5760)
	n := C.opus_decode(c.decoder, (*C.uchar)(unsafe.Pointer(&packet[0])),
		C.opus_int32(len(packet)), (*C.opus_int16)(unsafe.Pointer(&pcm[0])),
		C.int(len(pcm)), 0)
	if n < 0 {
		return nil, opusError(n)
	}
	return pcm[:int(n)], nil
}

func (c *codec) close() {
	C.opus_encoder_destroy(c.encoder)
	C.opus_decoder_destroy(c.decoder)
}
