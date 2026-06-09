// Package config loads/saves the standalone showstone.config.json (the password
// envelope + settings). Absent under the suite, where the aggregator owns the master
// key and injects a sub-key.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mykeep.ai/showstone/internal/secret"
)

type Config struct {
	SchemaVersion int             `json:"schema_version"`
	Secret        secret.Envelope `json:"secret"`
	Addr          string          `json:"addr"`
	Headless      bool            `json:"headless"`
	StrictDefault bool            `json:"strict_default"`
}

func Default() Config {
	return Config{SchemaVersion: 1, Addr: "127.0.0.1:8771"}
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func Exists(path string) bool { _, err := os.Stat(path); return err == nil }

func Save(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".showstone-cfg-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}
