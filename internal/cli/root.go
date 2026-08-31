package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/Leonz3n/devtask/internal/repo"
	"github.com/Leonz3n/devtask/internal/task"
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
	root.AddCommand(newTaskCommand(stdout), newTaskListCommand(stdout), newTaskAddCommand(stdout))
	return root
}

func newTaskAddCommand(stdout io.Writer) *cobra.Command {
	var base string
	command := &cobra.Command{
		Use:   "add <task> <alias>",
		Short: "Attach a Registered Repository to a Task",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(cmd, args); err != nil {
				return &validationError{err: err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			configuration, err := config.Load(paths.ConfigFile)
			if err != nil {
				return err
			}
			var baseOverride *string
			if cmd.Flags().Changed("base") {
				baseOverride = &base
			}
			result, err := task.AddRepository(paths, configuration, args[0], args[1], baseOverride)
			if err != nil {
				return err
			}
			action := "attached"
			if result.AlreadyAttached {
				action = "already attached"
			}
			_, err = fmt.Fprintf(stdout, "%s %s to Task %s at %s\n", action, result.Attachment.Alias, result.TaskName, result.Attachment.WorktreePath)
			return err
		},
	}
	command.Flags().StringVar(&base, "base", "", "local base branch for the new Task Branch")
	return command
}

func newTaskCommand(stdout io.Writer) *cobra.Command {
	var branch string
	command := &cobra.Command{
		Use:   "new <task>",
		Short: "Create an empty Task",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return &validationError{err: err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			configuration, err := config.Load(paths.ConfigFile)
			if err != nil {
				return err
			}
			var branchOverride *string
			if cmd.Flags().Changed("branch") {
				branchOverride = &branch
			}
			metadata, err := task.Create(paths, configuration, args[0], branchOverride)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "created Task %s with Task Branch Name %s\n", metadata.Name, metadata.TaskBranchName)
			return err
		},
	}
	command.Flags().StringVar(&branch, "branch", "", "override the Task Branch Name")
	return command
}

func newTaskListCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List Tasks",
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
			tasks, err := task.List(paths)
			if err != nil {
				return err
			}
			if asJSON {
				encoder := json.NewEncoder(stdout)
				encoder.SetEscapeHTML(false)
				return encoder.Encode(tasks)
			}
			for _, listedTask := range tasks {
				if _, err := fmt.Fprintf(stdout, "%s\t%d\t%s\n", listedTask.Name, listedTask.RepositoryCount, listedTask.CreatedAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return command
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
			_, err = fmt.Fprintf(stdout, "%s %s: %s\n", result.Action, result.Repository.Alias, result.Repository.Path)
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
	if errors.As(err, &validation) || errors.Is(err, config.ErrInvalid) || errors.Is(err, config.ErrNotInitialized) || errors.Is(err, task.ErrInvalid) {
		return 2
	}
	return 1
}
