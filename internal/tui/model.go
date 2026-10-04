package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/sadiksaifi/voice/internal/voice"
)

type Session interface {
	Events() <-chan voice.Event
	SetMuted(bool)
	Level() float64
}

type (
	tickMsg  time.Time
	endedMsg struct{}
	entry    struct{ role, text string }
)

type Model struct {
	session       Session
	width, height int
	status, plan  string
	muted, ended  bool
	transcript    []entry
	scroll        int
	started       time.Time
	err           error
}

func New(session Session) Model {
	return Model{
		session: session,
		width:   80,
		height:  24,
		status:  "Connecting",
		started: time.Now(),
	}
}

func tick() tea.Cmd {
	return tea.Tick(
		100*time.Millisecond,
		func(t time.Time) tea.Msg { return tickMsg(t) },
	)
}

func (m Model) wait() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.session.Events()
		if !ok {
			return endedMsg{}
		}
		return event
	}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.wait(), tick()) }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "space", "m":
			if !m.ended {
				m.muted = !m.muted
				m.session.SetMuted(m.muted)
			}
		case "up", "pgup":
			m.scroll += 3
		case "down", "pgdown":
			m.scroll = max(0, m.scroll-3)
		case "end":
			m.scroll = 0
		}
	case tickMsg:
		if !m.ended {
			return m, tick()
		}
	case endedMsg:
		m.ended = true
		m.status = "Stopped"
	case voice.Event:
		switch msg.Kind {
		case "status", "ready":
			m.status = msg.Text
		case "account":
			m.plan = msg.Text
		case "error":
			m.err = msg.Err
			m.status = "Connection error"
		case "transcript":
			if msg.Text != "" {
				n := len(m.transcript)
				if n > 0 && m.transcript[n-1].role == msg.Role {
					m.transcript[n-1].text += msg.Text
					if len(m.transcript[n-1].text) > 32768 {
						m.transcript[n-1].text = string(
							[]rune(m.transcript[n-1].text)[max(0, len([]rune(m.transcript[n-1].text))-8192):],
						)
					}
				} else {
					m.transcript = append(
						m.transcript,
						entry{role: msg.Role, text: msg.Text},
					)
				}
				if len(m.transcript) > 100 {
					m.transcript = m.transcript[len(m.transcript)-100:]
				}
			}
		}
		return m, m.wait()
	}
	return m, nil
}

func (m Model) View() tea.View {
	width := max(12, m.width-4)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#9293A4"))
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#BB9AF7")).
		Bold(true)
	human := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7DCFFF")).
		Bold(true)
	var body strings.Builder
	for _, item := range m.transcript {
		label := accent.Render(item.role)
		if item.role == "You" {
			label = human.Render(item.role)
		}
		body.WriteString(label + "\n")
		body.WriteString(
			lipgloss.NewStyle().Width(width).Render(ansi.Strip(item.text)),
		)
		body.WriteString("\n\n")
	}
	if len(m.transcript) == 0 {
		body.WriteString(
			muted.Render("Start speaking when the microphone is ready."),
		)
	}
	lines := strings.Split(body.String(), "\n")
	available := max(1, m.height-10)
	scroll := min(m.scroll, max(0, len(lines)-available))
	end := len(lines) - scroll
	start := max(0, end-available)
	content := strings.Join(lines[start:end], "\n")
	content = lipgloss.NewStyle().Height(available).Render(content)
	status := m.status
	if m.err != nil {
		status = m.err.Error()
	} else if m.muted {
		status = "Microphone muted"
	}
	level := int(min(1, m.session.Level()*4) * 20)
	meter := strings.Repeat("|", level) + strings.Repeat(".", 20-level)
	account := "ChatGPT"
	if m.plan != "" {
		account += " " + m.plan
	}
	header := accent.Render("Codex voice") + "  " + muted.Render(account)
	footer := muted.Render("space / m mute   up / down scroll   q quit")
	text := header + "\n" + muted.Render(
		strings.Repeat("─", width),
	) + "\n\n" + content + "\n" +
		lipgloss.NewStyle().
			Width(width).
			Render(status) +
		"\n" +
		muted.Render(
			fmt.Sprintf(
				"Mic [%s]  %s",
				meter,
				time.Since(m.started).Truncate(time.Second),
			),
		) + "\n\n" + footer
	view := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(text))
	view.AltScreen = true
	view.WindowTitle = "Codex voice"
	return view
}
