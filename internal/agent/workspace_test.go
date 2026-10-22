package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindRootFromSubdirectory is the point of the whole file: running
// pollington from deep inside a repository should still anchor at the top.
func TestFindRootFromSubdirectory(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(top, "internal", "tui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, start := range []string{top, filepath.Join(top, "internal"), deep} {
		if got := FindRoot(start); got != top {
			t.Errorf("FindRoot(%q) = %q, want %q", start, got, top)
		}
	}
}

// TestFindRootAcceptsGitFile covers worktrees and submodules, where .git is a
// file pointing elsewhere rather than a directory.
func TestFindRootAcceptsGitFile(t *testing.T) {
	top := t.TempDir()
	if err := os.WriteFile(filepath.Join(top, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(top, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindRoot(sub); got != top {
		t.Errorf("FindRoot = %q, want %q", got, top)
	}
}

// TestFindRootFallsBackToStart: with no repository above, the sandbox stays
// narrow rather than widening to a parent directory.
func TestFindRootFallsBackToStart(t *testing.T) {
	dir := t.TempDir()
	got := FindRoot(dir)

	// t.TempDir can sit under a symlinked path, so compare resolved forms.
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("FindRoot = %q, want %q", got, dir)
	}
	if parent := filepath.Dir(dir); got == parent {
		t.Error("fell back to the parent, widening the sandbox")
	}
}

// TestFindRootStopsAtNearestRepo: a nested repository wins over its parent.
func TestFindRootStopsAtNearestRepo(t *testing.T) {
	outer := t.TempDir()
	if err := os.Mkdir(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "vendored")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindRoot(inner); got != inner {
		t.Errorf("FindRoot = %q, want the nearest repo %q", got, inner)
	}
}

// TestSandboxIsRootNotWorkingDirectory is the security half. The sandbox is
// pinned to the root passed at construction, so it cannot be widened by
// changing the process working directory afterwards.
func TestSandboxIsRootNotWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}

	rf := readFile(root)

	// Even standing in the other directory, the tool must refuse it.
	chdir(t, outside)
	if _, err := rf.Run(context.Background(), json.RawMessage(`{"path":"secret.txt"}`)); err == nil {
		t.Error("read a file outside the root because the process had chdir'd there")
	}

	rel, err := filepath.Rel(root, filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Skip("no relative path between the temp dirs")
	}
	if _, err := rf.Run(context.Background(), json.RawMessage(`{"path":`+jsonString(rel)+`}`)); err == nil {
		t.Errorf("escaped the root via %q", rel)
	}
}

// TestShellRunsAtRoot: commands must execute where the build expects, not
// wherever the binary was launched.
func TestShellRunsAtRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, t.TempDir()) // stand somewhere else entirely

	out, err := runShell(root).Run(context.Background(), json.RawMessage(`{"command":"ls"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("command ran in the wrong directory: %q", out)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
