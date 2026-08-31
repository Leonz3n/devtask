package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type ExitError struct {
	Code int
}

func (err *ExitError) Error() string {
	return fmt.Sprintf("child process exited with code %d", err.Code)
}

func Run(executable, directory string, arguments []string, streams Streams) error {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return fmt.Errorf("resolve agent executable %q: %w", executable, err)
	}
	command := exec.Command(resolved, arguments...)
	command.Dir = directory
	command.Stdin = streams.Stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start agent executable %q: %w", executable, err)
	}

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for {
			select {
			case received := <-signals:
				_ = command.Process.Signal(received)
			case <-done:
				return
			}
		}
	}()
	err = command.Wait()
	signal.Stop(signals)
	close(done)
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return fmt.Errorf("wait for agent executable %q: %w", executable, err)
	}
	code := exitError.ExitCode()
	if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		code = 128 + int(status.Signal())
	}
	return &ExitError{Code: code}
}
