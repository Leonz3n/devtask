package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/Leonz3n/devtask/internal/repo"
	"github.com/spf13/cobra"
)

type validationError struct {
	err error
}

func (e *validationError) Error() string { return e.err.Error() }
func (e *validationError) Unwrap() error { return e.err }

func Execute() error {
	root := NewRootCommand(os.Stdout, os.Stderr)
	if _, _, err := root.Find(os.Args[1:]); err != nil {
		return &validationError{err: err}
	}
	return root.Execute()
}

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "devtask",
		Short:         "Coordinate development Tasks across Git repositories",
		Example:       "  devtask init",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Annotations["devtask.dev/requires-initialization"] == "false" {
				return nil
			}
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			_, err = config.Load(paths.ConfigFile)
			return err
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &validationError{err: err}
	})
	root.AddCommand(newInitCommand(stdout))
	root.AddCommand(newRepoCommand(stdout))
	return root
}

func newRepoCommand(stdout io.Writer) *cobra.Command {
	repository := &cobra.Command{Use: "repo", Short: "Manage Registered Repositories"}
	repository.AddCommand(newRepoAddCommand(stdout), newRepoListCommand(stdout))
	return repository
}

func newRepoAddCommand(stdout io.Writer) *cobra.Command {
	var update bool
	command := &cobra.Command{
		Use:   "add [--update] <alias> <path>",
		Short: "Register a local Git repository",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(cmd, args); err != nil {
				return &validationError{err: err}
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if err := config.ValidateRepositoryAlias(args[0]); err != nil {
				return err
			}
			mainCheckout, err := repo.ResolveMainCheckout(args[1])
			if err != nil {
				return &validationError{err: err}
			}
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			result, err := config.RegisterRepository(paths, args[0], mainCheckout, update)
			if err != nil {
				return err
			}
			action := "registered"
			if result.Updated {
				action = "updated"
			} else if result.Unchanged {
				action = "already registered"
			}
			_, err = fmt.Fprintf(stdout, "%s %s: %s\n", action, result.Repository.Alias, result.Repository.Path)
			return err
		},
	}
	command.Flags().BoolVar(&update, "update", false, "update an existing Repository Alias")
	return command
}

func newRepoListCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List Registered Repositories",
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
			repositories, err := config.ListRepositories(paths.ConfigFile)
			if err != nil {
				return err
			}
			if asJSON {
				encoder := json.NewEncoder(stdout)
				encoder.SetEscapeHTML(false)
				return encoder.Encode(repositories)
			}
			for _, repository := range repositories {
				if _, err := fmt.Fprintf(stdout, "%s\t%s\n", repository.Alias, repository.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return command
}

func newInitCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:         "init",
		Short:       "Initialize devtask local state",
		Annotations: map[string]string{"devtask.dev/requires-initialization": "false"},
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
	if errors.As(err, &validation) || errors.Is(err, config.ErrInvalid) || errors.Is(err, config.ErrNotInitialized) {
		return 2
	}
	return 1
}
