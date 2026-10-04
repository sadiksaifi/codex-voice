package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sadiksaifi/voice/internal/audio"
	"github.com/sadiksaifi/voice/internal/codex"
	"github.com/sadiksaifi/voice/internal/config"
	"github.com/sadiksaifi/voice/internal/tui"
	"github.com/sadiksaifi/voice/internal/voice"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("voice", flag.ContinueOnError)
	check := flags.Bool(
		"check",
		false,
		"Check subscription voice access without microphone or playback",
	)
	devices := flags.Bool("devices", false, "List microphones and speakers")
	flags.StringVar(&cfg.Codex, "codex", cfg.Codex, "Codex executable")
	flags.StringVar(
		&cfg.Directory,
		"cwd",
		cfg.Directory,
		"Codex working directory",
	)
	flags.StringVar(&cfg.Voice, "voice", cfg.Voice, "Voice name")
	flags.IntVar(
		&cfg.Microphone,
		"microphone",
		cfg.Microphone,
		"Microphone index from --devices (-1 selects default)",
	)
	flags.IntVar(
		&cfg.Speaker,
		"speaker",
		cfg.Speaker,
		"Speaker index from --devices (-1 selects default)",
	)
	if err = flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}
	if cfg.Microphone < -1 || cfg.Speaker < -1 {
		return fmt.Errorf("device indices must be -1 or greater")
	}
	if *devices {
		return listDevices()
	}
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
	cmd := exec.Command(
		cfg.Codex,
		"app-server",
		"--listen",
		"stdio://",
		"-c",
		`model_provider="openai"`,
		"-c",
		"features.plugins=false",
		"-c",
		"features.hooks=false",
		"-c",
		"features.shell_snapshot=false",
		"-c",
		"features.code_mode_host=false",
	)
	// Subscription-only: do not let inherited API credentials select API billing.
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if key != "OPENAI_API_KEY" && key != "CODEX_API_KEY" &&
			key != "CODEX_ACCESS_TOKEN" {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf(
			"start Codex (install Codex CLI and run codex login): %w",
			err,
		)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = stdout.Close()
	}()
	client := codex.NewClient(ctx, stdout, stdin)
	backend := codex.NewBackend(client, cfg.Directory)
	device, err := audio.New(*check, cfg.Microphone, cfg.Speaker)
	if err != nil {
		return err
	}
	defer device.Close()
	session := voice.New(backend, device, cfg.Voice)
	finished := make(chan error, 1)
	go func() { finished <- session.Run(ctx) }()
	if *check {
		checkErr := checkVoice(ctx, session)
		cancel()
		runErr := <-finished
		if checkErr != nil && runErr != nil &&
			!errors.Is(runErr, context.Canceled) {
			return runErr
		}
		return checkErr
	}
	_, uiErr := tea.NewProgram(tui.New(session), tea.WithContext(ctx)).Run()
	interrupted := ctx.Err() != nil
	cancel()
	sessionErr := <-finished
	if uiErr != nil && !errors.Is(uiErr, tea.ErrInterrupted) && !interrupted {
		return uiErr
	}
	if sessionErr != nil && !errors.Is(sessionErr, context.Canceled) {
		return sessionErr
	}
	return nil
}

func listDevices() error {
	microphones, speakers, err := audio.Devices()
	if err != nil {
		return err
	}
	for _, group := range []struct {
		name    string
		devices []audio.DeviceInfo
	}{{"Microphones", microphones}, {"Speakers", speakers}} {
		fmt.Println(group.name)
		for _, device := range group.devices {
			label := ""
			if device.Default {
				label = " (default)"
			}
			fmt.Printf("  %d  %s%s\n", device.Index, device.Name, label)
		}
	}
	return nil
}

func checkVoice(ctx context.Context, session *voice.Session) error {
	timer := time.NewTimer(75 * time.Second)
	defer timer.Stop()
	audioReceived, transcriptReceived := false, false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("voice check timed out")
		case event, ok := <-session.Events():
			if !ok {
				return fmt.Errorf(
					"voice session stopped before the check completed",
				)
			}
			switch event.Kind {
			case "error":
				return event.Err
			case "account":
				fmt.Printf("Authenticated with ChatGPT %s\n", event.Text)
			case "status", "ready":
				fmt.Println(event.Text)
				if event.Kind == "ready" {
					if err := session.SendText(
						ctx,
						"Please say hello in one short sentence. This is a voice connection check.",
					); err != nil {
						return err
					}
				}
			case "audio":
				audioReceived = true
			case "transcript":
				if event.Role == "Assistant" &&
					strings.TrimSpace(event.Text) != "" {
					transcriptReceived = true
				}
			}
			if audioReceived && transcriptReceived {
				fmt.Println(
					"PASS: WebRTC voice returned speech and transcripts using your ChatGPT login. No API key, microphone, or speaker was used.",
				)
				return nil
			}
		}
	}
}
