package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// withXDGHome pins XDG_CONFIG_HOME to a temp dir for the duration of the test
// so that we never touch the real ~/.config/bkfz/.
func withXDGHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	return tmp
}

func TestPath_RespectsXDGConfigHome(t *testing.T) {
	tmp := withXDGHome(t)

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "bkfz", "config.yaml")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestPath_FallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "bkfz", "config.yaml")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestLoad_ReturnsErrNotFoundWhenMissing(t *testing.T) {
	withXDGHome(t)

	_, err := Load()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	withXDGHome(t)

	want := &Config{
		SpaceDomain: "myteam.backlog.com",
		Projects:    []string{"PROJ", "OTHER"},
	}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSave_CreatesParentDirectoryWith0755(t *testing.T) {
	tmp := withXDGHome(t)

	c := &Config{SpaceDomain: "x.backlog.com"}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}

	parent := filepath.Join(tmp, "bkfz")
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory at %s", parent)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("dir perm = %o, want 0755", got)
		}
	}
}

func TestSave_WritesFileWith0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't behave the same on Windows")
	}
	withXDGHome(t)

	c := &Config{SpaceDomain: "x.backlog.com"}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file perm = %o, want 0600", got)
	}
}

func TestSave_OverwritesExistingFile(t *testing.T) {
	withXDGHome(t)

	first := &Config{SpaceDomain: "first.backlog.com", Projects: []string{"A"}}
	if err := Save(first); err != nil {
		t.Fatal(err)
	}
	second := &Config{SpaceDomain: "second.backlog.com", Projects: []string{"B", "C"}}
	if err := Save(second); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("after second save: got %+v, want %+v", got, second)
	}
}

func TestDataPath_RespectsXDGDataHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	got, err := DataPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "bkfz", "index.db")
	if got != want {
		t.Errorf("DataPath = %q, want %q", got, want)
	}
}

func TestDataPath_FallsBackToHomeLocalShare(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	got, err := DataPath()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "bkfz", "index.db")
	if got != want {
		t.Errorf("DataPath = %q, want %q", got, want)
	}
}

func TestLoad_RejectsInvalidYAML(t *testing.T) {
	tmp := withXDGHome(t)

	dir := filepath.Join(tmp, "bkfz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(bad, []byte("not: : valid: yaml: ::"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("expected parse error, got nil")
	}
}
