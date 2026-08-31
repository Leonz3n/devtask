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
