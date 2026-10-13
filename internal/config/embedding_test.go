package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	tmp := withXDGHome(t)
	dir := filepath.Join(tmp, "bkfz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_WithoutEmbeddingSectionLeavesItNil(t *testing.T) {
	writeConfig(t, "space_domain: example.backlog.com\nprojects: [PROJ]\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Embedding != nil {
		t.Errorf("Embedding should be nil when the section is absent, got %+v", c.Embedding)
	}
}

func TestLoad_EmbeddingAppliesDefaults(t *testing.T) {
	writeConfig(t, "space_domain: example.backlog.com\nembedding: {}\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e := c.Embedding
	if e == nil {
		t.Fatal("Embedding should be non-nil for an empty section")
	}
	if e.BaseURL != DefaultEmbeddingBaseURL || e.Model != DefaultEmbeddingModel {
		t.Errorf("defaults not applied: %+v", e)
	}
	if e.Dimensions != 0 {
		t.Errorf("Dimensions default should stay 0 (native), got %d", e.Dimensions)
	}
	if e.QueryPrefix == nil || *e.QueryPrefix != DefaultQueryPrefix {
		t.Errorf("QueryPrefix = %v, want default", e.QueryPrefix)
	}
	if e.DocumentPrefix == nil || *e.DocumentPrefix != DefaultDocumentPrefix {
		t.Errorf("DocumentPrefix = %v, want default", e.DocumentPrefix)
	}
	if e.MaxIssueChars != DefaultEmbeddingIssueChars || e.ChunkChars != DefaultEmbeddingChunkChars || e.MaxChunks != DefaultEmbeddingMaxChunks {
		t.Errorf("text defaults not applied: %+v", e)
	}
	if !e.IsLocal() {
		t.Error("default base_url should be local")
	}
}

func TestLoad_EmbeddingExplicitEmptyPrefixIsKept(t *testing.T) {
	writeConfig(t, "space_domain: x\nembedding:\n  query_prefix: \"\"\n  model: bge-m3\n  dimensions: 256\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Embedding.QueryPrefix == nil || *c.Embedding.QueryPrefix != "" {
		t.Errorf("explicit empty query_prefix should be kept, got %v", c.Embedding.QueryPrefix)
	}
	if c.Embedding.Model != "bge-m3" || c.Embedding.Dimensions != 256 {
		t.Errorf("explicit values overridden: %+v", c.Embedding)
	}
}

func TestLoad_EmbeddingRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"bad url":       "embedding:\n  base_url: localhost:11434\n",
		"negative dims": "embedding:\n  dimensions: -1\n",
		"blank model":   "embedding:\n  model: \"  \"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, "space_domain: x\n"+body)
			_, err := Load()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "embedding.") {
				t.Errorf("error should name the field, got %v", err)
			}
		})
	}
}

func TestEmbedding_IsLocal(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:11434/v1":    true,
		"http://127.0.0.1:8080/v1":     true,
		"http://[::1]:1234/v1":         true,
		"http://ollama.localhost/v1":   true,
		"https://api.openai.com/v1":    false,
		"http://192.168.1.10:11434/v1": false,
	}
	for u, want := range cases {
		if got := (Embedding{BaseURL: u}).IsLocal(); got != want {
			t.Errorf("IsLocal(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestEmbedding_APIKeyFromEnv(t *testing.T) {
	t.Setenv("BKFZ_TEST_EMBED_KEY", "secret")
	if got := (Embedding{APIKeyEnv: "BKFZ_TEST_EMBED_KEY"}).APIKey(); got != "secret" {
		t.Errorf("APIKey = %q", got)
	}
	if got := (Embedding{}).APIKey(); got != "" {
		t.Errorf("APIKey without env name = %q, want empty", got)
	}
}
