package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrBaseRefNotFound = errors.New("Base Ref not found")

type ResolvedBaseRef struct {
	Ref    string
	Commit string
}

func Run(directory string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(arguments, " "), message)
	}
	return output, nil
}

func RunPredicate(directory string, arguments ...string) (bool, error) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		return false, nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return false, fmt.Errorf("git %s: %s", strings.Join(arguments, " "), message)
}

func RefExists(directory, ref string) (bool, error) {
	return RunPredicate(directory, "show-ref", "--verify", "--quiet", ref)
}

func ValidateBranchName(name string) error {
	command := exec.Command("git", "check-ref-format", "--branch", name)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("invalid Task Branch Name %q: %s", name, message)
}

func ResolveBaseRef(directory, branch, remote string, fetch bool) (ResolvedBaseRef, error) {
	remoteExists, err := RemoteExists(directory, remote)
	if err != nil {
		return ResolvedBaseRef{}, fmt.Errorf("inspect configured remote %q: %w", remote, err)
	}
	if fetch && remoteExists {
		if _, err := Run(directory, "fetch", "--", remote); err != nil {
			return ResolvedBaseRef{}, fmt.Errorf("fetch configured remote %q: %w", remote, err)
		}
	}
	if remoteExists {
		remoteRef := "refs/remotes/" + remote + "/" + branch
		if resolved, found, err := resolveCommit(directory, remoteRef); err != nil {
			return ResolvedBaseRef{}, err
		} else if found {
			return resolved, nil
		}
	}
	localRef := "refs/heads/" + branch
	if resolved, found, err := resolveCommit(directory, localRef); err != nil {
		return ResolvedBaseRef{}, err
	} else if found {
		return resolved, nil
	}
	return ResolvedBaseRef{}, fmt.Errorf("%w: branch %q is absent from configured remote %q and local branches", ErrBaseRefNotFound, branch, remote)
}

func RemoteExists(directory, remote string) (bool, error) {
	if remote == "" {
		return false, nil
	}
	output, err := Run(directory, "remote")
	if err != nil {
		return false, err
	}
	for _, candidate := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if candidate == remote {
			return true, nil
		}
	}
	return false, nil
}

func resolveCommit(directory, ref string) (ResolvedBaseRef, bool, error) {
	exists, err := RefExists(directory, ref)
	if err != nil || !exists {
		return ResolvedBaseRef{}, false, err
	}
	output, err := Run(directory, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return ResolvedBaseRef{}, false, fmt.Errorf("resolve Base Ref %q: %w", ref, err)
	}
	return ResolvedBaseRef{Ref: ref, Commit: strings.TrimSpace(string(output))}, true, nil
}
