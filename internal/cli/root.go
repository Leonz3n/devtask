package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	root.AddCommand(newTaskCommand(stdout), newTaskListCommand(stdout), newTaskAddCommand(stdout, stderr), newTaskStatusCommand(stdout))
	return root
}

func newTaskStatusCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "status <task>",
		Short: "Report a Task and its Repository Attachments",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return &validationError{err: err}
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			report, err := task.Status(paths, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				encoder := json.NewEncoder(stdout)
				encoder.SetEscapeHTML(false)
				return encoder.Encode(report)
			}
			if _, err := fmt.Fprintf(stdout, "Task %s [%s]\n  Task Branch Name: %s\n  Created: %s\n  Task Workspace: %s\n", report.Name, formatTaskState(report), report.TaskBranchName, report.CreatedAt.Format(time.RFC3339), humanPath(report.WorkspacePath)); err != nil {
				return err
			}
			if report.Inspection != nil {
				if _, err := fmt.Fprintf(stdout, "  %s: %s\n", report.Inspection.Operation, report.Inspection.Message); err != nil {
					return err
				}
				for _, recovery := range report.Inspection.Recovery {
					if _, err := fmt.Fprintf(stdout, "  Recovery: %s\n", recovery); err != nil {
						return err
					}
				}
			}
			if report.IncompleteOperation != nil {
				if _, err := fmt.Fprintf(stdout, "  Incomplete operation: %s\n  Last error: %s\n", report.IncompleteOperation.Operation, report.IncompleteOperation.LastError); err != nil {
					return err
				}
				for _, residual := range report.IncompleteOperation.ResidualObjects {
					if _, err := fmt.Fprintf(stdout, "  Residual: %s\n", residual); err != nil {
						return err
					}
				}
				for _, recovery := range report.IncompleteOperation.Recovery {
					if _, err := fmt.Fprintf(stdout, "  Recovery: %s\n", recovery); err != nil {
						return err
					}
				}
			}
			for _, attachment := range report.Attachments {
				state := formatAttachmentState(attachment)
				if _, err := fmt.Fprintf(stdout, "  %s\t%s\t%s\n", attachment.Alias, state, humanPath(attachment.WorktreePath)); err != nil {
					return err
				}
				if attachment.Inspection != nil {
					if _, err := fmt.Fprintf(stdout, "    %s: %s\n", attachment.Inspection.Operation, attachment.Inspection.Message); err != nil {
						return err
					}
					for _, recovery := range attachment.Inspection.Recovery {
						if _, err := fmt.Fprintf(stdout, "    Recovery: %s\n", recovery); err != nil {
							return err
						}
					}
				}
				if attachment.LastError != "" {
					if _, err := fmt.Fprintf(stdout, "    Last error: %s\n", attachment.LastError); err != nil {
						return err
					}
				}
				for _, residual := range attachment.ResidualObjects {
					if _, err := fmt.Fprintf(stdout, "    Residual: %s\n", residual); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return command
}

func formatTaskState(report task.StatusReport) string {
	states := []string{string(report.State)}
	if report.Missing {
		states = append(states, "missing")
	}
	if report.Unknown {
		states = append(states, "unknown")
	}
	return strings.Join(states, ", ")
}

func humanPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(path) {
		return path
	}
	homes := []string{filepath.Clean(home)}
	if canonicalHome, canonicalError := filepath.EvalSymlinks(home); canonicalError == nil && canonicalHome != homes[0] {
		homes = append(homes, canonicalHome)
	}
	for _, candidate := range homes {
		relative, relativeError := filepath.Rel(candidate, path)
		if relativeError != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative == "." {
			return "~"
		}
		return filepath.Join("~", relative)
	}
	return path
}

func formatAttachmentState(attachment task.AttachmentStatus) string {
	states := make([]string, 0, 8)
	for _, candidate := range []struct {
		set  bool
		name string
	}{
		{attachment.Modified, "modified"},
		{attachment.Staged, "staged"},
		{attachment.Untracked, "untracked"},
		{attachment.Conflicted, "conflicted"},
		{attachment.Missing, "missing"},
		{attachment.Unknown, "unknown"},
		{attachment.Incomplete, "incomplete"},
	} {
		if candidate.set {
			states = append(states, candidate.name)
		}
	}
	if len(states) == 0 {
		return "clean"
	}
	return strings.Join(states, ", ")
}

func newTaskAddCommand(stdout, stderr io.Writer) *cobra.Command {
	var base string
	var fetch bool
	var noFetch bool
	command := &cobra.Command{
		Use:   "add <task> <alias>...",
		Short: "Attach Registered Repositories to a Task",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.MinimumNArgs(2)(cmd, args); err != nil {
				return &validationError{err: err}
			}
			if cmd.Flags().Changed("fetch") && cmd.Flags().Changed("no-fetch") {
				return &validationError{err: errors.New("--fetch and --no-fetch are mutually exclusive")}
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
			var fetchOverride *bool
			if cmd.Flags().Changed("fetch") {
				fetchOverride = &fetch
			} else if cmd.Flags().Changed("no-fetch") {
				override := !noFetch
				fetchOverride = &override
			}
			results, err := task.AddRepositories(paths, configuration, args[0], args[1:], baseOverride, fetchOverride)
			if err != nil {
				return err
			}
			for _, result := range results {
				for _, warning := range result.Warnings {
					if _, err = fmt.Fprintf(stderr, "warning: repository %s: %s\n", result.Attachment.Alias, warning); err != nil {
						return err
					}
				}
				action := "attached"
				if result.AlreadyAttached {
					action = "already attached"
				}
				if _, err = fmt.Fprintf(stdout, "%s %s to Task %s at %s\n", action, result.Attachment.Alias, result.TaskName, result.Attachment.WorktreePath); err != nil {
					return err
				}
				if !result.AlreadyAttached && result.Attachment.BranchExisted {
					if _, err = fmt.Fprintln(stdout, "existing Task Branch attached; Base Ref was not applied"); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "", "branch name used to resolve the Base Ref for a new Task Branch")
	command.Flags().BoolVar(&fetch, "fetch", false, "fetch the configured remote before resolving the Base Ref")
	command.Flags().BoolVar(&noFetch, "no-fetch", false, "use current refs without fetching")
	command.MarkFlagsMutuallyExclusive("fetch", "no-fetch")
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
