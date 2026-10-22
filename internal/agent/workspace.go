package agent

import (
	"os"
	"path/filepath"
)

// FindRoot resolves the directory the agent should treat as the project.
//
// It walks up from start looking for a .git entry, so running pollington deep
// inside a repository still anchors tools at the repository root - the model
// asks for "internal/tui/tui.go" regardless of where you launched it from, and
// a shell command runs where the build expects to run.
//
// A .git *file* counts as well as a directory: that is what a worktree or a
// submodule has.
//
// With no repository anywhere above start, start itself is the root. That keeps
// the tool usable in a scratch directory, and it is the conservative choice -
// the sandbox stays narrow rather than widening to a parent.
func FindRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding a repository.
			abs, err := filepath.Abs(start)
			if err != nil {
				return start
			}
			return abs
		}
		dir = parent
	}
}
