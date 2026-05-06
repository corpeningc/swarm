// Package config loads ~/.swarm/config.yaml (or $SWARM_HOME/config.yaml) and
// exposes typed access to user preferences, agent profiles, and cost rates.
package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	Home     string
	Profiles map[string]Profile
}

type Profile struct {
	Agent string
	Args  []string
	Env   map[string]string
}

func Home() string {
	if h := os.Getenv("SWARM_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".swarm")
	}
	return ".swarm"
}
