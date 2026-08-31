package repo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	bare, err := git(canonicalInput, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", fmt.Errorf("repository path %q is not inside a Git repository: %w", input, err)
	}
	if bare == "true" {
		return "", fmt.Errorf("repository path %q is a bare repository; register a Main Checkout", input)
	}
	worktrees, err := git(canonicalInput, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("find Main Checkout for %q: %w", input, err)
	}
	firstLine, _, _ := strings.Cut(worktrees, "\n")
	if !strings.HasPrefix(firstLine, "worktree ") {
		return "", fmt.Errorf("find Main Checkout for %q: Git returned no main worktree", input)
	}
	mainCheckout := strings.TrimPrefix(firstLine, "worktree ")
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

func git(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(arguments, " "), message)
	}
	return strings.TrimSpace(string(output)), nil
}
