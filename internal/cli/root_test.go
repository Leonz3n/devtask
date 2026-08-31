package cli

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/spf13/cobra"
)

func TestUninitializedConfigurationUsesValidationExitCode(t *testing.T) {
	err := fmt.Errorf("load configuration: %w", config.ErrNotInitialized)

	if code := ExitCode(err); code != 2 {
		t.Fatalf("ExitCode() = %d, want 2", code)
	}
}

func TestCommandThatRequiresInitializationFailsBeforeRunning(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ran := false
	root := NewRootCommand(&stdout, &stderr)
	root.AddCommand(&cobra.Command{
		Use: "probe",
		Run: func(_ *cobra.Command, _ []string) {
			ran = true
		},
	})
	root.SetArgs([]string{"probe"})

	err := root.Execute()

	if ran {
		t.Fatal("command ran without initialized configuration")
	}
	if !errors.Is(err, config.ErrNotInitialized) {
		t.Fatalf("Execute() error = %v, want ErrNotInitialized", err)
	}
	if code := ExitCode(err); code != 2 {
		t.Fatalf("ExitCode() = %d, want 2", code)
	}
	for _, expected := range []string{filepath.Join(configHome, "devtask", "config.yaml"), "run 'devtask init'"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Execute() error = %q, want it to contain %q", err, expected)
		}
	}
}
