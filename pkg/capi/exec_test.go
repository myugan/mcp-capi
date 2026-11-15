package capi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stdoutOf returns a result's stdout, or "" when the command never ran.
func stdoutOf(result *ExecResult) string {
	if result == nil {
		return ""
	}
	return result.Stdout
}

// stubBinary writes an executable shell script and returns its path.
func stubBinary(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub script requires a POSIX shell")
	}
	script := filepath.Join(t.TempDir(), name)
	// #nosec G306 -- the stub stands in for a CLI binary, so it has to be
	// executable; it lives in the test's own temp dir.
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("failed to write stub %s: %v", name, err)
	}
	return script
}

func TestRunCLIKeepsStdoutAndStderrApart(t *testing.T) {
	bin := stubBinary(t, "cli", "echo '{\"items\":[]}'\necho 'Warning: deprecated' >&2\n")

	result, err := runCLI(context.Background(), bin, nil, ExecOptions{})
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}
	if result.Failed() || result.ExitCode != 0 {
		t.Fatalf("a clean exit must not be a failure, got %+v", result)
	}
	if result.Stdout != "{\"items\":[]}\n" || result.Stderr != "Warning: deprecated\n" {
		t.Fatalf("stdout = %q, stderr = %q", result.Stdout, result.Stderr)
	}
}

// A command that fails is a result: its exit code and what it wrote are what
// the caller needs to see.
func TestRunCLINonZeroExitIsAResult(t *testing.T) {
	bin := stubBinary(t, "cli", "echo partial\necho 'Error from server (NotFound): pods \"x\" not found' >&2\nexit 3\n")

	result, err := runCLI(context.Background(), bin, nil, ExecOptions{})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error, got %v", err)
	}
	if !result.Failed() || result.ExitCode != 3 || result.TimedOut {
		t.Fatalf("got %+v, want exit code 3", result)
	}
	if result.Stdout != "partial\n" || !strings.Contains(result.Stderr, "NotFound") {
		t.Fatalf("output must be kept, stdout = %q, stderr = %q", result.Stdout, result.Stderr)
	}
}

// A command that never exits (logs -f, get -w) is stopped at the timeout and
// returns what it had printed, instead of holding the call.
func TestRunCLIStopsAtTheTimeoutAndKeepsOutput(t *testing.T) {
	bin := stubBinary(t, "cli", "echo 'line before the wait'\nexec sleep 60\n")

	start := time.Now()
	result, err := runCLI(context.Background(), bin, nil, ExecOptions{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("a timeout must not be an error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the process was not stopped: took %s", elapsed)
	}
	if !result.TimedOut || !result.Failed() || result.Timeout != 300*time.Millisecond {
		t.Fatalf("got %+v, want a timeout", result)
	}
	if result.Stdout != "line before the wait\n" {
		t.Fatalf("output printed before the timeout must be kept, got %q", result.Stdout)
	}
}

func TestRunCLICallerCancellationIsAnError(t *testing.T) {
	bin := stubBinary(t, "cli", "exec sleep 60\n")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if result, err := runCLI(ctx, bin, nil, ExecOptions{Timeout: time.Minute}); err == nil {
		t.Fatalf("expected the caller's cancellation as an error, got %+v", result)
	}
}

func TestRunCLIMissingBinaryIsAnError(t *testing.T) {
	if _, err := runCLI(context.Background(), filepath.Join(t.TempDir(), "absent"), nil, ExecOptions{}); err == nil {
		t.Fatal("expected an error for a binary that does not exist")
	}
}

// helm's "--values -" reads stdin: before ExecHelm passed any, the values
// never arrived.
func TestExecHelmPipesStdin(t *testing.T) {
	original := helmBinaryPath
	helmBinaryPath = stubBinary(t, "helm", "echo \"ARGS: $*\"\necho STDIN-BEGIN\ncat\necho STDIN-END\n")
	t.Cleanup(func() { helmBinaryPath = original })
	c := newKubectlTestClient(t, "default", "timbernetes")

	values := "replicaCount: 2\n"
	result, err := c.ExecHelm(context.Background(), "default", "timbernetes",
		[]string{"upgrade", "--install", "web", "repo/web", "--values", "-"}, ExecOptions{Stdin: values})
	if err != nil {
		t.Fatalf("ExecHelm failed: %v", err)
	}
	if !strings.Contains(result.Stdout, "ARGS: --kubeconfig") || !strings.Contains(result.Stdout, "upgrade --install web repo/web --values -") {
		t.Errorf("expected helm to get the kubeconfig and the args as given, got: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "STDIN-BEGIN\n"+values+"STDIN-END") {
		t.Errorf("expected the values on helm's stdin, got: %s", result.Stdout)
	}

	if _, err := c.ExecHelm(context.Background(), "default", "timbernetes",
		[]string{"list", "--kube-context", "other"}, ExecOptions{}); err == nil {
		t.Error("expected a target-overriding flag to be rejected")
	}
}
