package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingConfigurationExplainsHowToInitialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	_, err := Load(path)

	if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Load error = %v, want ErrNotInitialized", err)
	}
	for _, expected := range []string{path, "run 'devtask init'"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Load error = %q, want it to contain %q", err, expected)
		}
	}
}
