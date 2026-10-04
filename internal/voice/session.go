package voice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

type Event struct {
	Kind string
	Role string
	Text string
	Err  error
}

type Backend interface {
	Start(context.Context, string, string) (string, string, error)
	SendText(context.Context, string) error
	Stop(context.Context) error
	Events() <-chan Event
	Decode([]byte) []Event
}

type Audio interface {
	Start() error
	Frames() <-chan []byte
	Errors() <-chan error
	Play([]byte) (bool, error)
	ClearPlayback()
	SetMuted(bool)
	Level() float64
}

type Session struct {
	backend Backend
	audio   Audio
	voice   string
	events  chan Event
	mu      sync.RWMutex
	closed  bool
}

func New(backend Backend, audio Audio, selectedVoice string) *Session {
	return &Session{
		backend: backend,
		audio:   audio,
		voice:   selectedVoice,
		events:  make(chan Event, 128),
	}
}

func (s *Session) Events() <-chan Event { return s.events }
func (s *Session) SetMuted(muted bool)  { s.audio.SetMuted(muted) }
func (s *Session) Level() float64       { return s.audio.Level() }
func (s *Session) SendText(ctx context.Context, text string) error {
	return s.backend.SendText(ctx, text)
}

func (s *Session) emit(ctx context.Context, event Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- event:
	case <-ctx.Done():
	}
}

func (s *Session) Run(parent context.Context) (runErr error) {
	ctx, cancel := context.WithCancel(parent)
	defer func() {
		cancel()
		s.mu.Lock()
		if runErr != nil && parent.Err() == nil {
			select {
			case s.events <- Event{Kind: "error", Err: runErr}:
			default:
			}
		}
		s.closed = true
		close(s.events)
		s.mu.Unlock()
	}()
	s.emit(ctx, Event{Kind: "status", Text: "Connecting to Codex"})
	engine := &webrtc.MediaEngine{}
	err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio)
	if err != nil {
		return err
	}
	registry := &interceptor.Registry{}
	if err = webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
		return err
	}
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(engine),
		webrtc.WithInterceptorRegistry(registry),
	)
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	ready := make(chan struct{})
	failures := make(chan error, 1)
	fail := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed {
			fail(fmt.Errorf("voice connection failed"))
		}
	})
	var workers sync.WaitGroup
	var trackMu sync.Mutex
	closing := false
	defer func() {
		cancel()
		trackMu.Lock()
		closing = true
		trackMu.Unlock()
		_ = peer.Close()
		workers.Wait()
	}()
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		trackMu.Lock()
		if closing {
			trackMu.Unlock()
			return
		}
		workers.Add(1)
		trackMu.Unlock()
		defer workers.Done()
		builder := samplebuilder.New(10, &codecs.OpusPacket{}, 48000)
		announced := false
		for {
			packet, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}
			builder.Push(packet)
			for sample := builder.Pop(); sample != nil; sample = builder.Pop() {
				audible, playErr := s.audio.Play(sample.Data)
				if playErr != nil {
					fail(playErr)
					return
				}
				if audible && !announced {
					announced = true
					s.emit(ctx, Event{Kind: "audio", Text: "Receiving audio"})
				}
			}
		}
	})
	channel, err := peer.CreateDataChannel("oai-events", nil)
	if err != nil {
		return err
	}
	var readyOnce sync.Once
	channel.OnMessage(
		func(message webrtc.DataChannelMessage) {
			for _, event := range s.backend.Decode(message.Data) {
				if event.Kind == "connected" {
					readyOnce.Do(func() { close(ready) })
				} else if event.Err != nil {
					fail(event.Err)
				} else if event.Kind == "interrupt" {
					s.audio.ClearPlayback()
				} else {
					s.emit(ctx, event)
				}
			}
		},
	)
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		"microphone",
		"voice",
	)
	if err != nil {
		return err
	}
	sender, err := peer.AddTrack(track)
	if err != nil {
		return err
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		buffer := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(buffer); readErr != nil {
				return
			}
		}
	}()
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(offer); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return ctx.Err()
	}
	answer, plan, err := s.backend.Start(
		ctx,
		peer.LocalDescription().SDP,
		s.voice,
	)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer stopCancel()
		_ = s.backend.Stop(stopCtx)
	}()
	s.emit(ctx, Event{Kind: "account", Text: plan})
	if err = peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer},
	); err != nil {
		return err
	}
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case err = <-failures:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out connecting voice; check UDP connectivity")
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = s.audio.Start(); err != nil {
		return err
	}
	s.emit(ctx, Event{Kind: "ready", Text: "Listening"})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-failures:
			return err
		case err = <-s.audio.Errors():
			return err
		case packet, ok := <-s.audio.Frames():
			if !ok {
				return fmt.Errorf("audio capture stopped")
			}
			if err = track.WriteSample(
				media.Sample{Data: packet, Duration: 20 * time.Millisecond},
			); err != nil {
				return err
			}
		case event, ok := <-s.backend.Events():
			if !ok {
				return fmt.Errorf("codex disconnected")
			}
			if event.Err != nil {
				return event.Err
			}
			s.emit(ctx, event)
		}
	}
}
