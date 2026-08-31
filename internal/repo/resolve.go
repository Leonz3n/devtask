package repo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	devgit "github.com/Leonz3n/devtask/internal/git"
)

func ResolveMainCheckout(input string) (string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("inspect repository path %q: %w", input, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %q is not a directory", input)
	}
	canonicalInput, err := filepath.EvalSymlinks(input)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %q: %w", input, err)
	}
	bareOutput, err := devgit.Run(canonicalInput, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", fmt.Errorf("repository path %q is not inside a Git repository: %w", input, err)
	}
	if strings.TrimSpace(string(bareOutput)) == "true" {
		return "", fmt.Errorf("repository path %q is a bare repository; register a Main Checkout", input)
	}
	worktrees, err := devgit.Run(canonicalInput, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("find Main Checkout for %q: %w", input, err)
	}
	mainCheckout, err := firstMainCheckout(worktrees)
	if err != nil {
		return "", fmt.Errorf("find Main Checkout for %q: Git returned no main worktree", input)
	}
	canonical, err := filepath.EvalSymlinks(mainCheckout)
	if err != nil {
		return "", fmt.Errorf("resolve Main Checkout %q: %w", mainCheckout, err)
	}
	absolute, err := filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("resolve Main Checkout %q: %w", mainCheckout, err)
	}
	return filepath.Clean(absolute), nil
}

func firstMainCheckout(output []byte) (string, error) {
	field, _, found := bytes.Cut(output, []byte{0})
	const prefix = "worktree "
	if !found || !bytes.HasPrefix(field, []byte(prefix)) || len(field) == len(prefix) {
		return "", fmt.Errorf("missing worktree field")
	}
	return string(field[len(prefix):]), nil
}
