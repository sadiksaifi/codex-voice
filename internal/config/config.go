package config

import "os"

type Config struct {
	Codex      string
	Directory  string
	Voice      string
	Microphone int
	Speaker    int
}

func Load() (Config, error) {
	directory, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Codex: "codex", Directory: directory, Voice: "juniper",
		Microphone: -1, Speaker: -1,
	}, nil
}
