package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the user configuration for bkfz.
// The API key is read from the BACKLOG_API_KEY environment variable, so
// it is intentionally not stored here.
type Config struct {
	SpaceDomain string   `yaml:"space_domain"` // e.g. "myteam.backlog.com"
	Projects    []string `yaml:"projects"`     // project keys to sync (empty = all projects)
}

// ErrNotFound is returned by Load when no config file exists.
var ErrNotFound = errors.New("config not found")

// File names under the XDG directories.
const (
	configDirName  = "bkfz"
	configFileName = "config.yaml"
	dbFileName     = "index.db"
	dirMode        = 0o755
	fileMode       = 0o600
)

// Path returns the config file path following XDG conventions.
// Uses XDG_CONFIG_HOME if set, otherwise falls back to $HOME/.config
// (we use .config consistently, including on macOS).
func Path() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, configDirName, configFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: home dir: %w", err)
	}
	return filepath.Join(home, ".config", configDirName, configFileName), nil
}

// DataPath returns the DB file path following XDG conventions.
// Uses XDG_DATA_HOME if set, otherwise falls back to $HOME/.local/share.
// Example: ~/.local/share/bkfz/index.db
func DataPath() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, configDirName, dbFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", configDirName, dbFileName), nil
}

// Load reads the YAML file at Path(). Returns ErrNotFound if the file is missing.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes Config to Path(). The parent directory is created as needed.
// The file is written with mode 0600 (owner read/write only).
func Save(c *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, fileMode); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}
