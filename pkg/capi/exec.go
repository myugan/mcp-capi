package capi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultExecTimeout bounds a kubectl or helm run when the caller sets none.
// A command that follows or watches (logs -f, get -w, port-forward) never
// exits by itself; without a bound it holds the tool call until the transport
// gives up and returns nothing.
const DefaultExecTimeout = 2 * time.Minute

// MaxExecTimeout is the longest run a caller may ask for.
const MaxExecTimeout = 10 * time.Minute

// execWaitDelay is how long a stopped process gets to close its pipes before
// they are closed for it, so a child that outlives the kill cannot hold the
// call open.
const execWaitDelay = 2 * time.Second

// ExecOptions are the optional inputs of ExecKubectl and ExecHelm.
type ExecOptions struct {
	// Stdin, when non-empty, is piped to the process. There is no local
	// filesystem to read a file from, so this is how content reaches
	// "apply -f -" or "--values -".
	Stdin string
	// Timeout stops the process when it runs longer. Zero means
	// DefaultExecTimeout.
	Timeout time.Duration
}

// ExecResult is what a kubectl or helm process left behind. Stdout and Stderr
// are kept apart: stdout is the answer (and stays parseable when it is JSON or
// YAML), stderr is where the tool explains a failure or warns.
type ExecResult struct {
	Stdout string
	Stderr string
	// ExitCode is the process's exit code; -1 when it was stopped.
	ExitCode int
	// TimedOut reports that the process was stopped after Timeout. Stdout and
	// Stderr hold what it had written by then.
	TimedOut bool
	Timeout  time.Duration
}

// Failed reports whether the command did not complete successfully.
func (r *ExecResult) Failed() bool {
	return r.TimedOut || r.ExitCode != 0
}

// runCLI runs binary with args and returns what it wrote and how it ended. A
// process that exits non-zero or is stopped at the timeout is a result, not
// an error: the caller needs its output to say what went wrong. An error
// means the process could not be run at all, or the caller's own context
// ended.
func runCLI(ctx context.Context, binary string, args []string, opts ExecOptions) (*ExecResult, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- binary is one of this package's fixed paths to a binary
	// in the image, and the callers check every element of args against their
	// blocked flags before they get here.
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.WaitDelay = execWaitDelay
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), Timeout: timeout}
	if err == nil {
		return result, nil
	}

	if ctx.Err() != nil {
		// The caller went away; there is nobody to hand the output to.
		return nil, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return nil, fmt.Errorf("failed to run %s: %w", binary, err)
}
