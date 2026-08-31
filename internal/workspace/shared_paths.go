package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Leonz3n/devtask/internal/fileutil"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
)

type SharedLocalPathProjection struct {
	plans    []sharedLocalPathPlan
	warnings []string
}

type sharedLocalPathPlan struct {
	configuredPath string
	link           ManagedLink
	sourceInfo     os.FileInfo
	created        bool
}

func PrepareSharedLocalPaths(mainCheckout, worktreePath string, configured []string) (*SharedLocalPathProjection, error) {
	projection := &SharedLocalPathProjection{
		plans:    make([]sharedLocalPathPlan, 0, len(configured)),
		warnings: make([]string, 0),
	}
	canonicalMainCheckout, err := filepath.EvalSymlinks(mainCheckout)
	if err != nil {
		return nil, fmt.Errorf("resolve Main Checkout for Shared Local Paths: %w", err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Task Worktree for Shared Local Paths: %w", err)
	}
	plannedDestinations := make(map[string]struct{}, len(configured))
	for _, configuredPath := range configured {
		plan, warning, err := prepareSharedLocalPath(canonicalMainCheckout, canonicalWorktree, configuredPath)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			projection.warnings = append(projection.warnings, warning)
			continue
		}
		if _, duplicate := plannedDestinations[plan.link.Destination]; duplicate {
			projection.warnings = append(projection.warnings, sharedPathWarning(configuredPath, "is configured more than once"))
			continue
		}
		plannedDestinations[plan.link.Destination] = struct{}{}
		projection.plans = append(projection.plans, plan)
	}
	return projection, nil
}

func prepareSharedLocalPath(mainCheckout, worktreePath, configuredPath string) (sharedLocalPathPlan, string, error) {
	cleaned, valid := cleanRelativeSharedPath(configuredPath)
	if !valid {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "must be relative and remain inside the Main Checkout"), nil
	}
	source := filepath.Join(mainCheckout, cleaned)
	sourceInfo, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "source does not exist"), nil
	}
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("inspect Shared Local Path source %q: %w", source, err)
	}
	contained, err := entryRemainsWithin(mainCheckout, source, sourceInfo.Mode()&os.ModeSymlink != 0)
	if err != nil {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "source cannot be resolved safely"), nil
	}
	if !contained {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "source escapes the Main Checkout"), nil
	}
	destination := filepath.Join(worktreePath, cleaned)
	if _, err := os.Lstat(destination); err == nil {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination already exists"), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return sharedLocalPathPlan{}, "", fmt.Errorf("inspect Shared Local Path destination %q: %w", destination, err)
	}
	contained, err = absentEntryRemainsWithin(worktreePath, destination)
	if errors.Is(err, os.ErrNotExist) {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination parent does not exist"), nil
	}
	if err != nil {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination cannot be resolved safely"), nil
	}
	if !contained {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination escapes the Task Worktree"), nil
	}
	tracked, err := gitcmd.PathTracked(mainCheckout, cleaned)
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("check Shared Local Path source tracking for %q: %w", configuredPath, err)
	}
	if tracked {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "source is tracked"), nil
	}
	tracked, err = gitcmd.PathTracked(worktreePath, cleaned)
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("check Shared Local Path destination tracking for %q: %w", configuredPath, err)
	}
	if tracked {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination is tracked"), nil
	}
	ignorePath := cleaned
	if sourceInfo.IsDir() {
		ignorePath += string(filepath.Separator)
	}
	ignored, err := gitcmd.PathIgnored(mainCheckout, ignorePath)
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("check Shared Local Path source ignore for %q: %w", configuredPath, err)
	}
	if !ignored {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "source is not effectively ignored"), nil
	}
	ignored, err = gitcmd.PathIgnored(worktreePath, cleaned)
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("check Shared Local Path destination ignore for %q: %w", configuredPath, err)
	}
	if !ignored {
		return sharedLocalPathPlan{}, sharedPathWarning(configuredPath, "destination is not effectively ignored"), nil
	}
	target, err := filepath.Rel(filepath.Dir(destination), source)
	if err != nil {
		return sharedLocalPathPlan{}, "", fmt.Errorf("calculate relative Shared Local Path target for %q: %w", configuredPath, err)
	}
	return sharedLocalPathPlan{
		configuredPath: configuredPath,
		link:           ManagedLink{Source: source, Destination: destination, Target: target},
		sourceInfo:     sourceInfo,
	}, "", nil
}

func (projection *SharedLocalPathProjection) Commit() error {
	for index := range projection.plans {
		plan := &projection.plans[index]
		currentSource, err := os.Lstat(plan.link.Source)
		if errors.Is(err, os.ErrNotExist) {
			projection.warnings = append(projection.warnings, sharedPathWarning(plan.configuredPath, "source disappeared during projection"))
			continue
		}
		if err != nil {
			projection.warnings = append(projection.warnings, sharedPathWarning(plan.configuredPath, "source cannot be rechecked safely"))
			continue
		}
		if !os.SameFile(plan.sourceInfo, currentSource) {
			projection.warnings = append(projection.warnings, sharedPathWarning(plan.configuredPath, "source changed during projection"))
			continue
		}
		if _, err := os.Lstat(plan.link.Destination); err == nil {
			projection.warnings = append(projection.warnings, sharedPathWarning(plan.configuredPath, "destination appeared during projection"))
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recheck Shared Local Path destination %q: %w", plan.link.Destination, err)
		}
		if err := os.Symlink(plan.link.Target, plan.link.Destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				projection.warnings = append(projection.warnings, sharedPathWarning(plan.configuredPath, "destination appeared during projection"))
				continue
			}
			return fmt.Errorf("create Shared Local Path link %q: %w", plan.link.Destination, err)
		}
		plan.created = true
	}
	for _, directory := range projection.createdDirectories() {
		if err := fileutil.SyncDirectory(directory); err != nil {
			return fmt.Errorf("sync Shared Local Path directory %q: %w", directory, err)
		}
	}
	return nil
}

func (projection *SharedLocalPathProjection) Abort() error {
	var failures []error
	directories := projection.createdDirectories()
	for index := len(projection.plans) - 1; index >= 0; index-- {
		plan := &projection.plans[index]
		if !plan.created {
			continue
		}
		target, err := os.Readlink(plan.link.Destination)
		if errors.Is(err, os.ErrNotExist) {
			plan.created = false
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("inspect Shared Local Path link during rollback %q: %w", plan.link.Destination, err))
			continue
		}
		if target != plan.link.Target {
			failures = append(failures, fmt.Errorf("refuse to remove changed Shared Local Path link %q", plan.link.Destination))
			continue
		}
		if err := os.Remove(plan.link.Destination); err != nil {
			failures = append(failures, fmt.Errorf("remove Shared Local Path link %q: %w", plan.link.Destination, err))
			continue
		}
		plan.created = false
	}
	for _, directory := range directories {
		if err := fileutil.SyncDirectory(directory); err != nil {
			failures = append(failures, fmt.Errorf("sync Shared Local Path directory after rollback %q: %w", directory, err))
		}
	}
	return errors.Join(failures...)
}

func (projection *SharedLocalPathProjection) ManagedLinks() []ManagedLink {
	links := make([]ManagedLink, 0, len(projection.plans))
	for _, plan := range projection.plans {
		if plan.created {
			links = append(links, plan.link)
		}
	}
	return links
}

func (projection *SharedLocalPathProjection) Warnings() []string {
	return append([]string(nil), projection.warnings...)
}

func (projection *SharedLocalPathProjection) createdDirectories() []string {
	seen := make(map[string]struct{})
	directories := make([]string, 0)
	for _, plan := range projection.plans {
		if !plan.created {
			continue
		}
		directory := filepath.Dir(plan.link.Destination)
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	return directories
}

func cleanRelativeSharedPath(path string) (string, bool) {
	cleaned := filepath.Clean(path)
	return cleaned, path != "" && !filepath.IsAbs(path) && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func entryRemainsWithin(root, entry string, allowFinalSymlink bool) (bool, error) {
	path := entry
	if allowFinalSymlink {
		parent, err := filepath.EvalSymlinks(filepath.Dir(entry))
		if err != nil {
			return false, err
		}
		path = filepath.Join(parent, filepath.Base(entry))
	} else {
		resolved, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return false, err
		}
		path = resolved
	}
	return pathWithin(root, path)
}

func absentEntryRemainsWithin(root, entry string) (bool, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(entry))
	if err != nil {
		return false, err
	}
	return pathWithin(root, filepath.Join(parent, filepath.Base(entry)))
}

func pathWithin(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func sharedPathWarning(path, reason string) string {
	return fmt.Sprintf("Shared Local Path %q skipped: %s", path, reason)
}
