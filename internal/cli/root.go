package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/spf13/cobra"
)

type validationError struct {
	err error
}

func (e *validationError) Error() string { return e.err.Error() }
func (e *validationError) Unwrap() error { return e.err }

func Execute() error {
	return NewRootCommand(os.Stdout, os.Stderr).Execute()
}

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "devtask",
		Short:         "Coordinate development Tasks across Git repositories",
		Example:       "  devtask init",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &validationError{err: err}
	})
	root.AddCommand(newInitCommand(stdout))
	return root
}

func newInitCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize devtask local state",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return &validationError{err: err}
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			if err := config.Initialize(paths); err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "initialized devtask\nconfig: %s\ndata: %s\n", paths.ConfigFile, paths.DataDir)
			return err
		},
	}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var validation *validationError
	if errors.As(err, &validation) || errors.Is(err, config.ErrInvalid) {
		return 2
	}
	return 1
}
