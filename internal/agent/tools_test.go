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

// chdir points the process at dir for the duration of the test, which is what
// safePath resolves against. testing.T.Chdir does this natively but needs Go
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
	chdir(t, t.TempDir())
	w := writeFile()

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
	chdir(t, t.TempDir())

	if _, err := run(t, writeFile(), `{"path":"a/b/c/deep.txt","content":"x"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join("a", "b", "c", "deep.txt")); err != nil {
		t.Errorf("nested path not created: %v", err)
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile("x.txt", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, writeFile(), `{"path":"x.txt","content":"new"}`); err != nil {
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
	chdir(t, t.TempDir())
	if err := os.WriteFile("exists.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := writeFile()

	if d := w.Describe(json.RawMessage(`{"path":"exists.txt","content":"y"}`)); !strings.Contains(d, "overwrite") {
		t.Errorf("describe = %q, want 'overwrite'", d)
	}
	if d := w.Describe(json.RawMessage(`{"path":"brand-new.txt","content":"y"}`)); !strings.Contains(d, "create") {
		t.Errorf("describe = %q, want 'create'", d)
	}
}

func TestWriteFileRejectsEscape(t *testing.T) {
	chdir(t, t.TempDir())

	for _, bad := range []string{"../escaped.txt", "../../etc/passwd", "a/../../out.txt"} {
		if _, err := run(t, writeFile(), `{"path":"`+bad+`","content":"x"}`); err == nil {
			t.Errorf("path %q was accepted, should be rejected", bad)
		}
	}
}

func TestReadFileRejectsEscape(t *testing.T) {
	chdir(t, t.TempDir())

	if _, err := run(t, readFile(), `{"path":"../../../etc/passwd"}`); err == nil {
		t.Error("read outside the working directory was allowed")
	}
}

func TestRunShellCapturesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	chdir(t, t.TempDir())

	out, err := run(t, runShell(), `{"command":"echo hello"}`)
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
	chdir(t, t.TempDir())

	out, err := run(t, runShell(), `{"command":"echo to-stderr >&2; exit 3"}`)
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
	if err := os.WriteFile("marker.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, runShell(), `{"command":"ls"}`)
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
	chdir(t, t.TempDir())

	old := shellTimeout
	shellTimeout = 150 * time.Millisecond
	t.Cleanup(func() { shellTimeout = old })

	start := time.Now()
	out, err := run(t, runShell(), `{"command":"sleep 30"}`)
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
	chdir(t, t.TempDir())

	if _, err := run(t, runShell(), `{"command":"   "}`); err == nil {
		t.Error("an empty command should be rejected")
	}
}

func TestToolsRejectMalformedArguments(t *testing.T) {
	chdir(t, t.TempDir())

	// Small models do emit broken JSON here, so every tool must return a
	// readable error rather than panicking.
	for _, tool := range []Tool{readFile(), writeFile(), runShell()} {
		name := tool.Def.Function.Name
		if _, err := run(t, tool, `{"path": broken`); err == nil {
			t.Errorf("%s accepted malformed JSON", name)
		}
	}
}

func TestListDirMarksDirectories(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("file.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, listDir(), `{}`)
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
