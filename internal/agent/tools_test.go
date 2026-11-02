package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// run is a helper that invokes a tool's implementation directly, bypassing the
// confirmation gate (which has its own tests).
func run(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	return tool.Run(context.Background(), json.RawMessage(args))
}

// sandbox returns a fresh directory to use as the project root, and points the
// process at it so tests can use relative paths when checking the disk.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)
	return dir
}

// chdir points the process at dir for the duration of the test, so relative
// paths in assertions resolve there. testing.T.Chdir does this natively but needs Go
// 1.24, and the module floor is 1.23 - there is no reason to raise the version
// every user needs for a test convenience. Working directory is process-wide, so
// these tests must not call t.Parallel.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Errorf("restoring working directory: %v", err)
		}
	})
}

func TestWriteFileWritesToDisk(t *testing.T) {
	root := sandbox(t)
	w := writeFile(root)

	out, err := run(t, w, `{"path":"hello.txt","content":"line one\nline two\n"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Errorf("result %q should name the file", out)
	}

	got, err := os.ReadFile("hello.txt")
	if err != nil {
		t.Fatalf("file was not created: %v", err)
	}
	if string(got) != "line one\nline two\n" {
		t.Errorf("contents = %q", got)
	}
}

func TestWriteFileCreatesParentDirs(t *testing.T) {
	root := sandbox(t)

	if _, err := run(t, writeFile(root), `{"path":"a/b/c/deep.txt","content":"x"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join("a", "b", "c", "deep.txt")); err != nil {
		t.Errorf("nested path not created: %v", err)
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	root := sandbox(t)
	if err := os.WriteFile("x.txt", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, writeFile(root), `{"path":"x.txt","content":"new"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	got, _ := os.ReadFile("x.txt")
	if string(got) != "new" {
		t.Errorf("contents = %q, want new", got)
	}
}

// TestWriteFileDescribeDistinguishesCreateFromOverwrite matters for the
// confirmation prompt: "overwrite main.go" deserves more care than "create".
func TestWriteFileDescribeDistinguishesCreateFromOverwrite(t *testing.T) {
	root := sandbox(t)
	if err := os.WriteFile("exists.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := writeFile(root)

	if d := w.Describe(json.RawMessage(`{"path":"exists.txt","content":"y"}`)); !strings.Contains(d, "overwrite") {
		t.Errorf("describe = %q, want 'overwrite'", d)
	}
	if d := w.Describe(json.RawMessage(`{"path":"brand-new.txt","content":"y"}`)); !strings.Contains(d, "create") {
		t.Errorf("describe = %q, want 'create'", d)
	}
}

func TestWriteFileRejectsEscape(t *testing.T) {
	root := sandbox(t)

	for _, bad := range []string{"../escaped.txt", "../../etc/passwd", "a/../../out.txt"} {
		if _, err := run(t, writeFile(root), `{"path":"`+bad+`","content":"x"}`); err == nil {
			t.Errorf("path %q was accepted, should be rejected", bad)
		}
	}
}

func TestReadFileRejectsEscape(t *testing.T) {
	root := sandbox(t)

	if _, err := run(t, readFile(root), `{"path":"../../../etc/passwd"}`); err == nil {
		t.Error("read outside the working directory was allowed")
	}
}

func TestRunShellCapturesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := sandbox(t)

	out, err := run(t, runShell(root), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("run_shell: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("output = %q, want hello", out)
	}
}

// TestRunShellReportsFailureWithoutError is the design decision worth locking
// down: a non-zero exit is not a Go error, because the model needs to read the
// failing output in order to fix it.
func TestRunShellReportsFailureWithoutError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := sandbox(t)

	out, err := run(t, runShell(root), `{"command":"echo to-stderr >&2; exit 3"}`)
	if err != nil {
		t.Fatalf("a failing command must not be a Go error: %v", err)
	}
	if !strings.Contains(out, "to-stderr") {
		t.Errorf("stderr was lost: %q", out)
	}
	if !strings.Contains(out, "command failed") {
		t.Errorf("output %q should note the failure", out)
	}
}

func TestRunShellRunsInWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	chdir(t, dir)
	root := dir
	if err := os.WriteFile("marker.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, runShell(root), `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("run_shell: %v", err)
	}
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("command did not run in the working directory: %q", out)
	}
}

func TestRunShellTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := sandbox(t)

	old := shellTimeout
	shellTimeout = 150 * time.Millisecond
	t.Cleanup(func() { shellTimeout = old })

	start := time.Now()
	out, err := run(t, runShell(root), `{"command":"sleep 30"}`)
	if err != nil {
		t.Fatalf("a timeout must come back as a readable result: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s - the command was not killed", elapsed)
	}
	if !strings.Contains(out, "killed") {
		t.Errorf("output %q should say it was killed", out)
	}
}

func TestRunShellRejectsEmpty(t *testing.T) {
	root := sandbox(t)

	if _, err := run(t, runShell(root), `{"command":"   "}`); err == nil {
		t.Error("an empty command should be rejected")
	}
}

func TestToolsRejectMalformedArguments(t *testing.T) {
	root := sandbox(t)

	// Small models do emit broken JSON here, so every tool must return a
	// readable error rather than panicking.
	for _, tool := range []Tool{readFile(root), writeFile(root), runShell(root)} {
		name := tool.Def.Function.Name
		if _, err := run(t, tool, `{"path": broken`); err == nil {
			t.Errorf("%s accepted malformed JSON", name)
		}
	}
}

func TestListDirMarksDirectories(t *testing.T) {
	root := sandbox(t)
	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("file.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, listDir(root), `{}`)
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if !strings.Contains(out, "sub/") {
		t.Errorf("directories should be marked with a slash: %q", out)
	}
	if !strings.Contains(out, "file.txt") {
		t.Errorf("missing file: %q", out)
	}
}

// TestSymlinkOutOfTheRootIsRejected is the hole a textual containment check
// leaves: "link/secret.txt" has no ".." in it and reads as a path under the
// root, so without resolving links first it sails through.
func TestSymlinkOutOfTheRootIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	root := sandbox(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("stolen"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if out, err := run(t, readFile(root), `{"path":"link/secret.txt"}`); err == nil {
		t.Errorf("read outside the root through a symlink: %q", out)
	}
	if _, err := run(t, listDir(root), `{"path":"link"}`); err == nil {
		t.Error("listed a directory outside the root through a symlink")
	}
	if _, err := run(t, writeFile(root), `{"path":"link/planted.txt","content":"x"}`); err == nil {
		t.Error("write outside the root through a symlink was accepted")
	}
	// The refusal has to be the file never appearing, not just an error string.
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Error("the file was written outside the project root")
	}
}

// TestSymlinkInsideTheRootStillWorks guards the other direction: resolving
// links must not start refusing the ordinary reason repositories contain them.
func TestSymlinkInsideTheRootStillWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	root := sandbox(t)
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "f.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, readFile(root), `{"path":"alias/f.txt"}`)
	if err != nil {
		t.Fatalf("a symlink within the root should still resolve: %v", err)
	}
	if out != "ok" {
		t.Errorf("contents = %q, want ok", out)
	}
}
