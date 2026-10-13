package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the user configuration for bkfz.
// The API key is read from the BACKLOG_API_KEY environment variable, so
// it is intentionally not stored here.
type Config struct {
	SpaceDomain string   `yaml:"space_domain"` // e.g. "myteam.backlog.com"
	Projects    []string `yaml:"projects"`     // project keys to sync (empty = all projects)
	// Embedding enables semantic search when set. nil = disabled, in which
	// case bkfz behaves exactly as before (no extra tables, no HTTP calls).
	Embedding *Embedding `yaml:"embedding,omitempty"`
}

// Embedding configures the embedding server used for semantic search.
// Only OpenAI-compatible servers (`POST {base_url}/embeddings`) are
// supported; the defaults target a local Ollama.
type Embedding struct {
	BaseURL    string `yaml:"base_url"`             // e.g. "http://localhost:11434/v1"
	Model      string `yaml:"model"`                // e.g. "embeddinggemma"
	Dimensions int    `yaml:"dimensions,omitempty"` // 0 = model's native size
	// APIKeyEnv names the environment variable holding a bearer token.
	// The key itself is never stored in the config.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
	// QueryPrefix / DocumentPrefix are prepended to query / document text
	// (model-specific instructions). nil = model default (see Defaults).
	QueryPrefix    *string `yaml:"query_prefix,omitempty"`
	DocumentPrefix *string `yaml:"document_prefix,omitempty"`
	MaxIssueChars  int     `yaml:"max_issue_chars,omitempty"` // description runes embedded per issue
	ChunkChars     int     `yaml:"chunk_chars,omitempty"`     // document chunk size in runes
	MaxChunks      int     `yaml:"max_chunks,omitempty"`      // chunks embedded per document
	// TUIMode selects what Ctrl-S runs: "semantic" (default) or "hybrid"
	// (keyword + semantic fused with Reciprocal Rank Fusion).
	TUIMode string `yaml:"tui_mode,omitempty"`
}

// TUI modes accepted by Embedding.TUIMode.
const (
	TUIModeSemantic = "semantic"
	TUIModeHybrid   = "hybrid"
)

// Embedding defaults. The prefixes follow the EmbeddingGemma model card
// ("task: search result | query: ..." for queries, none for documents).
const (
	DefaultEmbeddingBaseURL    = "http://localhost:11434/v1"
	DefaultEmbeddingModel      = "embeddinggemma"
	DefaultQueryPrefix         = "task: search result | query: "
	DefaultDocumentPrefix      = ""
	DefaultEmbeddingIssueChars = 1200
	DefaultEmbeddingChunkChars = 1000
	DefaultEmbeddingMaxChunks  = 8
)

// ErrNotFound is returned by Load when no config file exists.
var ErrNotFound = errors.New("config not found")

// WithDefaults returns a copy with empty fields replaced by the defaults.
// Load applies it automatically; it is exported for tests and for code
// that constructs an Embedding by hand.
func (e Embedding) WithDefaults() Embedding {
	if e.BaseURL == "" {
		e.BaseURL = DefaultEmbeddingBaseURL
	}
	if e.Model == "" {
		e.Model = DefaultEmbeddingModel
	}
	if e.QueryPrefix == nil {
		s := DefaultQueryPrefix
		e.QueryPrefix = &s
	}
	if e.DocumentPrefix == nil {
		s := DefaultDocumentPrefix
		e.DocumentPrefix = &s
	}
	if e.MaxIssueChars == 0 {
		e.MaxIssueChars = DefaultEmbeddingIssueChars
	}
	if e.ChunkChars == 0 {
		e.ChunkChars = DefaultEmbeddingChunkChars
	}
	if e.MaxChunks == 0 {
		e.MaxChunks = DefaultEmbeddingMaxChunks
	}
	if e.TUIMode == "" {
		e.TUIMode = TUIModeSemantic
	}
	return e
}

// Validate reports the first invalid field. It expects WithDefaults to
// have been applied, so empty strings are only possible for api_key_env.
func (e Embedding) Validate() error {
	u, err := url.Parse(e.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("config: embedding.base_url must be an http(s) URL, got %q", e.BaseURL)
	}
	if strings.TrimSpace(e.Model) == "" {
		return errors.New("config: embedding.model is required")
	}
	if e.Dimensions < 0 {
		return fmt.Errorf("config: embedding.dimensions must be >= 0, got %d", e.Dimensions)
	}
	if e.MaxIssueChars < 0 || e.ChunkChars < 0 || e.MaxChunks < 0 {
		return errors.New("config: embedding.max_issue_chars / chunk_chars / max_chunks must be >= 0")
	}
	if e.TUIMode != TUIModeSemantic && e.TUIMode != TUIModeHybrid {
		return fmt.Errorf("config: embedding.tui_mode must be %q or %q, got %q", TUIModeSemantic, TUIModeHybrid, e.TUIMode)
	}
	return nil
}

// IsLocal reports whether base_url points at this machine. Used to warn
// before Backlog content is sent to an external service.
func (e Embedding) IsLocal() bool {
	u, err := url.Parse(e.BaseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// APIKey returns the bearer token from the environment variable named by
// api_key_env, or "" when unset.
func (e Embedding) APIKey() string {
	if e.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(e.APIKeyEnv)
}

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
	if c.Embedding != nil {
		e := c.Embedding.WithDefaults()
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("%w (in %s)", err, path)
		}
		c.Embedding = &e
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
