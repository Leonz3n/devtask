package git

import (
	"fmt"
	"os/exec"
	"strings"
)

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

func Success(directory string, arguments ...string) (bool, error) {
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
	return Success(directory, "show-ref", "--verify", "--quiet", ref)
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
