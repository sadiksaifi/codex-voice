package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

type Device struct {
	context   *malgo.AllocatedContext
	device    *malgo.Device
	codec     *codec
	synthetic bool
	muted     atomic.Bool
	level     atomic.Uint64
	input     chan []int16
	frames    chan []byte
	errors    chan error
	stop      chan struct{}
	done      chan struct{}
	started   bool
	mu        sync.Mutex
	playback  playbackBuffer
}

type DeviceInfo struct {
	Index   int
	Name    string
	Default bool
}

func Devices() (microphones, speakers []DeviceInfo, err error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = ctx.Uninit(); ctx.Free() }()
	for _, kind := range []malgo.DeviceType{malgo.Capture, malgo.Playback} {
		infos, queryErr := ctx.Devices(kind)
		if queryErr != nil {
			return nil, nil, queryErr
		}
		for i, info := range infos {
			item := DeviceInfo{
				Index:   i,
				Name:    info.Name(),
				Default: info.IsDefault != 0,
			}
			if kind == malgo.Capture {
				microphones = append(microphones, item)
			} else {
				speakers = append(speakers, item)
			}
		}
	}
	return microphones, speakers, nil
}

// New prepares the selected devices. Synthetic mode never opens audio hardware.
func New(synthetic bool, microphone, speaker int) (*Device, error) {
	c, err := newCodec()
	if err != nil {
		return nil, err
	}
	d := &Device{
		codec: c, synthetic: synthetic, input: make(chan []int16, 20),
		frames: make(chan []byte, 20), errors: make(chan error, 1),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if synthetic {
		return d, nil
	}
	d.context, err = malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		c.close()
		return nil, err
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Duplex)
	cfg.Capture.Format, cfg.Playback.Format = malgo.FormatS16, malgo.FormatS16
	cfg.Capture.Channels, cfg.Playback.Channels = 1, 1
	cfg.SampleRate = sampleRate
	cfg.PeriodSizeInMilliseconds = 20
	for _, selection := range []struct {
		kind  malgo.DeviceType
		index int
	}{{malgo.Capture, microphone}, {malgo.Playback, speaker}} {
		if selection.index < 0 {
			continue
		}
		infos, queryErr := d.context.Devices(selection.kind)
		if queryErr != nil {
			d.Close()
			return nil, queryErr
		}
		if selection.index >= len(infos) {
			d.Close()
			return nil, fmt.Errorf(
				"audio device index %d is unavailable",
				selection.index,
			)
		}
		if selection.kind == malgo.Capture {
			cfg.Capture.DeviceID = infos[selection.index].ID.Pointer()
			defer freeDeviceID(cfg.Capture.DeviceID)
		} else {
			cfg.Playback.DeviceID = infos[selection.index].ID.Pointer()
			defer freeDeviceID(cfg.Playback.DeviceID)
		}
	}
	d.device, err = malgo.InitDevice(
		d.context.Context,
		cfg,
		malgo.DeviceCallbacks{Data: d.callback},
	)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("open microphone and speaker: %w", err)
	}
	return d, nil
}

func (d *Device) callback(output, input []byte, _ uint32) {
	clear(output)
	d.mu.Lock()
	d.playback.read(output)
	d.mu.Unlock()
	pcm := make([]int16, len(input)/2)
	peak := 0
	if !d.muted.Load() {
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(input[i*2:]))
			peak = max(peak, int(math.Abs(float64(pcm[i]))))
		}
	}
	d.level.Store(math.Float64bits(float64(peak) / 32768))
	select {
	case d.input <- pcm:
	default:
	}
}

func (d *Device) Start() error {
	if d.started {
		return fmt.Errorf("audio already started")
	}
	d.started = true
	go d.encode()
	if d.device != nil {
		if err := d.device.Start(); err != nil {
			return fmt.Errorf("start microphone: %w", err)
		}
	}
	return nil
}

func (d *Device) encode() {
	defer close(d.done)
	defer close(d.frames)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	pcm := make([]int16, 0, frameSamples*2)
	for {
		select {
		case <-d.stop:
			return
		case chunk := <-d.input:
			pcm = append(pcm, chunk...)
		case <-ticker.C:
			if !d.synthetic {
				continue
			}
			pcm = append(pcm, make([]int16, frameSamples)...)
		}
		for len(pcm) >= frameSamples {
			if d.muted.Load() {
				clear(pcm[:frameSamples])
			}
			packet, err := d.codec.encode(pcm[:frameSamples])
			if err != nil {
				d.errors <- err
				return
			}
			pcm = pcm[frameSamples:]
			select {
			case d.frames <- packet:
			case <-d.stop:
				return
			}
		}
	}
}

func (d *Device) Frames() <-chan []byte { return d.frames }
func (d *Device) Errors() <-chan error  { return d.errors }
func (d *Device) SetMuted(muted bool)   { d.muted.Store(muted) }

func (d *Device) Level() float64 { return math.Float64frombits(d.level.Load()) }

func (d *Device) ClearPlayback() {
	d.mu.Lock()
	d.playback.clear()
	d.mu.Unlock()
}

// Play decodes one Opus packet and reports whether it contains audible samples.
func (d *Device) Play(packet []byte) (bool, error) {
	pcm, err := d.codec.decode(packet)
	if err != nil {
		return false, err
	}
	audible := false
	for _, sample := range pcm {
		if sample > 100 || sample < -100 {
			audible = true
			break
		}
	}
	if !d.synthetic {
		d.mu.Lock()
		d.playback.push(pcm)
		d.mu.Unlock()
	}
	return audible, nil
}

func (d *Device) Close() {
	if d.device != nil {
		d.device.Uninit()
	}
	close(d.stop)
	if d.started {
		<-d.done
	}
	if d.context != nil {
		_ = d.context.Uninit()
		d.context.Free()
	}
	d.codec.close()
}
