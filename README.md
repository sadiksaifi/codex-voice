# Codex voice

A Go terminal client for Codex voice using your ChatGPT subscription, with no separate API key.

## Prerequisites

- [mise](https://mise.jdx.dev/installing-mise.html)
- [Codex CLI](https://developers.openai.com/codex/cli/)

## Setup

From the project directory:

```sh
mise install
mise run build
```

## Run

Start the interactive client:

```sh
mise run start
```

Allow microphone access when macOS asks. Start speaking once the status says
"Listening". Use headphones: this prototype does not include echo cancellation.

| Key | Action |
| --- | --- |
| Space or m | Mute or unmute microphone |
| Up / Down, PgUp / PgDn | Scroll transcript |
| End | Return to latest transcript |
| q, Esc, Ctrl+C | Stop voice and quit |

Select devices or another voice:

```sh
./bin/voice --devices
./bin/voice --microphone 0 --speaker 0 --voice juniper
./bin/voice --help
```

Voice support is experimental and depends on the installed Codex version and
account access. Tested with Codex CLI 0.160.0 and ChatGPT Pro. WebSocket realtime
rejected ChatGPT authentication in that version; WebRTC v3 succeeded.

The demo uses an ephemeral, read-only Codex thread. Requests to approve file
changes, expand permissions, or authorize commands are declined. Transcript text
is held in memory and displayed in the terminal; this client does not save it.
