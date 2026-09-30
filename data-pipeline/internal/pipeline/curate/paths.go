package curate

import (
	"os"
	"path/filepath"
	"strings"
)

// moduleDirName is the repository directory name declared in go.mod, used to
// recognise the repository root while walking up from the working directory.
const moduleDirName = "platepilot"

// maxRootDepth bounds the upward walk so a deeply nested or symlinked layout
// cannot spin.
const maxRootDepth = 8

// RepoRoot returns the repository root directory.
//
// Configured paths such as the boundary file are written repo-relative, which is
// what a person editing .env expects. Resolving them against the working
// directory alone would break `go test ./...` (which runs in the package
// directory) and any invocation from a subdirectory, so a relative path is
// retried against the repository root before it is reported missing.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < maxRootDepth; i++ {
		if isRepoRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

func isRepoRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	// The module path ends in the repository directory name; checking the
	// directory name is enough to avoid latching onto an unrelated go.mod.
	// The comparison is case-insensitive because the checkout is usually
	// capitalised ("PlatePilot") while the module path is lower case.
	return strings.EqualFold(filepath.Base(dir), moduleDirName)
}

// ResolvePath expands a configured path against the repository root when it is
// relative and does not already exist relative to the working directory.
//
// An absolute path, or a relative path that resolves from the working
// directory, is returned unchanged, so an explicit location always wins.
func ResolvePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return trimmed
	}
	if _, err := os.Stat(trimmed); err == nil {
		return trimmed
	}
	root, err := RepoRoot()
	if err != nil {
		return trimmed
	}
	candidate := filepath.Join(root, trimmed)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return trimmed
}
