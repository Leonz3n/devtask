package launcher

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Leonz3n/devtask/internal/config"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
	"github.com/Leonz3n/devtask/internal/lock"
	"github.com/Leonz3n/devtask/internal/runner"
	"github.com/Leonz3n/devtask/internal/task"
)

type AgentLauncher string

const (
	Pi     AgentLauncher = "pi"
	Claude AgentLauncher = "claude"
	Codex  AgentLauncher = "codex"
)

var ErrInvalid = errors.New("invalid Agent Launcher request")

type repositoryLockTarget struct {
	path  string
	alias string
}

func Launch(paths config.Paths, configuration config.Config, agentLauncher AgentLauncher, taskName string, arguments []string, streams runner.Streams) error {
	if err := task.ValidateName(taskName); err != nil {
		return err
	}
	if err := validateForwardedArguments(agentLauncher, arguments); err != nil {
		return err
	}
	taskLock, err := lock.AcquireShared(task.LockPath(paths, taskName))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return fmt.Errorf("Task %q is busy: another devtask process holds its lock", taskName)
		}
		return err
	}
	defer taskLock.Close()

	metadata, err := task.Load(paths, taskName)
	if err != nil {
		return err
	}
	if metadata.State != task.StateReady {
		return fmt.Errorf("%w: Task %q is incomplete; run status and follow recovery guidance before using an Agent Launcher", task.ErrInvalid, metadata.Name)
	}

	targetsByPath := make(map[string]repositoryLockTarget, len(metadata.Attachments))
	for _, attachment := range metadata.Attachments {
		path, err := gitcmd.RepositoryLockPath(attachment.MainCheckout)
		if err != nil {
			return fmt.Errorf("locate Registered Repository lock for %q: %w", attachment.Alias, err)
		}
		if _, exists := targetsByPath[path]; !exists {
			targetsByPath[path] = repositoryLockTarget{path: path, alias: attachment.Alias}
		}
	}
	targets := make([]repositoryLockTarget, 0, len(targetsByPath))
	for _, target := range targetsByPath {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
	repositoryLocks := make([]*lock.File, 0, len(targets))
	defer func() {
		for index := len(repositoryLocks) - 1; index >= 0; index-- {
			_ = repositoryLocks[index].Close()
		}
	}()
	for _, target := range targets {
		repositoryLock, err := lock.AcquireShared(target.path)
		if err != nil {
			if errors.Is(err, lock.ErrBusy) {
				return fmt.Errorf("Registered Repository %q is busy: another devtask process holds its lock", target.alias)
			}
			return err
		}
		repositoryLocks = append(repositoryLocks, repositoryLock)
	}

	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)
	invocation := buildInvocation(agentLauncher, configuration, metadata, workspacePath, arguments)
	return runner.Run(invocation, streams)
}

func buildInvocation(agentLauncher AgentLauncher, configuration config.Config, metadata task.Metadata, workspacePath string, arguments []string) runner.Invocation {
	attachments := append([]task.RepositoryAttachment(nil), metadata.Attachments...)
	sort.SliceStable(attachments, func(i, j int) bool { return attachments[i].Order < attachments[j].Order })
	invocation := runner.Invocation{Directory: workspacePath, Arguments: arguments}
	switch agentLauncher {
	case Claude:
		invocation.Executable = configuration.Agents.Claude.Command
	case Codex:
		invocation.Executable = configuration.Agents.Codex.Command
	default:
		invocation.Executable = configuration.Agents.Pi.Command
	}
	if len(attachments) == 0 || agentLauncher == Pi {
		return invocation
	}

	switch agentLauncher {
	case Claude:
		launcherArguments := make([]string, 0, len(arguments)+len(attachments))
		for _, attachment := range attachments[1:] {
			launcherArguments = append(launcherArguments, "--add-dir="+attachment.WorktreePath)
		}
		launcherArguments = append(launcherArguments, "--add-dir="+workspacePath)
		launcherArguments = append(launcherArguments, arguments...)
		invocation.Directory = attachments[0].WorktreePath
		invocation.Arguments = launcherArguments
	case Codex:
		launcherArguments := []string{"-C", attachments[0].WorktreePath}
		for _, attachment := range attachments[1:] {
			launcherArguments = append(launcherArguments, "--add-dir", attachment.WorktreePath)
		}
		launcherArguments = append(launcherArguments, "--add-dir", workspacePath)
		launcherArguments = append(launcherArguments, arguments...)
		invocation.Arguments = launcherArguments
	}
	return invocation
}

func validateForwardedArguments(agentLauncher AgentLauncher, arguments []string) error {
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		switch agentLauncher {
		case Claude:
			if argument == "-w" || strings.HasPrefix(argument, "-w=") || argument == "--worktree" || strings.HasPrefix(argument, "--worktree=") {
				return fmt.Errorf("%w: Claude --worktree conflicts with the devtask-managed Task Worktree", ErrInvalid)
			}
		case Codex:
			if strings.HasPrefix(argument, "-C") || argument == "--cd" || strings.HasPrefix(argument, "--cd=") {
				return fmt.Errorf("%w: Codex %s conflicts with the devtask-managed working root", ErrInvalid, argument)
			}
		}
	}
	return nil
}
