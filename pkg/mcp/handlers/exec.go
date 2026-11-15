package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// capi_kubectl and capi_helm return what the command printed, not a JSON
// document: the output is already the caller's chosen format (a table, YAML,
// JSON, a log), and wrapping it would escape every line of it.
//
// What they add is on the edges. A command that fails or is stopped is an
// error result that leads with the reason. Output that is larger than a
// reader can use is cut, visibly. And two kinds of bulk that carry nothing
// are taken out: a log line repeated many times in a row, and the
// last-applied-configuration annotation in a manifest.

const (
	timeoutSecondsDescription = "Seconds before the command is stopped (default 120, max 600). " +
		"One that follows or watches is stopped then, with what it printed"

	// stderrLimit caps the stderr of a successful command: warnings, not the answer.
	stderrLimit = 2000

	// logsHeadPercent is the share of the output limit that goes to the start
	// of a log; the end, where the failure is, gets the rest.
	logsHeadPercent = 20

	// minRepeatsToCollapse is how many identical lines in a row are worth
	// replacing with a count.
	minRepeatsToCollapse = 3

	verbLogs = "logs"
	verbGet  = "get"

	formatYAML = "yaml"
	formatJSON = "json"
)

// execArguments reads the arguments capi_kubectl and capi_helm share: the
// command's own args, stdin, the timeout and the output limit.
func execArguments(arguments map[string]any) (args []string, opts capi.ExecOptions, limit int, err error) {
	rawArgs, ok := arguments["args"].([]any)
	if !ok || len(rawArgs) == 0 {
		return nil, opts, 0, fmt.Errorf("args argument is required and must be a non-empty array of strings")
	}
	args = make([]string, 0, len(rawArgs))
	for _, a := range rawArgs {
		s, ok := a.(string)
		if !ok {
			return nil, opts, 0, fmt.Errorf("all elements of args must be strings")
		}
		args = append(args, s)
	}

	opts.Stdin, _ = arguments["stdin"].(string)

	if raw, ok := arguments["timeout_seconds"]; ok && raw != nil {
		maxSeconds := capi.MaxExecTimeout.Seconds()
		n, ok := raw.(float64)
		if !ok || n < 0 || n > maxSeconds || n != float64(int(n)) {
			return nil, opts, 0, fmt.Errorf("timeout_seconds must be a whole number from 1 to %d", int(maxSeconds))
		}
		opts.Timeout = time.Duration(n) * time.Second
	}

	limit, err = outputLimit(arguments)
	if err != nil {
		return nil, opts, 0, err
	}
	return args, opts, limit, nil
}

// kubectlVerb returns the kubectl command in args: the first argument that is
// not a flag, skipping the value of a namespace flag given before it.
func kubectlVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-n" || arg == "--namespace" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

// outputFormat returns the value of kubectl's -o/--output flag in args, in
// any of its spellings (-o yaml, -oyaml, -o=yaml, --output yaml,
// --output=yaml), or "" when there is none.
func outputFormat(args []string) string {
	for i, arg := range args {
		switch {
		case arg == "-o" || arg == "--output":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "--output="):
			return strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-o="):
			return strings.TrimPrefix(arg, "-o=")
		case strings.HasPrefix(arg, "-o") && !strings.HasPrefix(arg, "--"):
			return strings.TrimPrefix(arg, "-o")
		}
	}
	return ""
}

// collapseRepeats replaces a run of identical lines with the line once and a
// count of the rest. A container that fails in a loop prints the same line
// hundreds of times; the count says as much as the copies.
func collapseRepeats(output string) string {
	lines := strings.SplitAfter(output, "\n")
	var out strings.Builder
	out.Grow(len(output))
	for i := 0; i < len(lines); {
		line := strings.TrimSuffix(lines[i], "\n")
		j := i + 1
		for j < len(lines) && strings.TrimSuffix(lines[j], "\n") == line && lines[j] != "" {
			j++
		}
		run := j - i
		if run < minRepeatsToCollapse || strings.TrimSpace(line) == "" {
			for k := i; k < j; k++ {
				out.WriteString(lines[k])
			}
		} else {
			out.WriteString(line)
			fmt.Fprintf(&out, "\n[previous line repeated %d more times]\n", run-1)
		}
		i = j
	}
	return out.String()
}

// stripLastApplied removes the kubectl.kubernetes.io/last-applied-configuration
// annotation from a manifest printed by kubectl as YAML or JSON. The
// annotation is a second copy of the object, as one long string, and often
// the larger half of the output. It works on kubectl's own formatting, line
// by line, so the rest of the document is returned byte for byte.
func stripLastApplied(output, format string) string {
	if !strings.Contains(output, corev1.LastAppliedConfigAnnotation) {
		return output
	}
	lines := strings.SplitAfter(output, "\n")
	kept := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)

		switch format {
		case formatYAML:
			if !strings.HasPrefix(trimmed, corev1.LastAppliedConfigAnnotation+":") {
				kept = append(kept, line)
				continue
			}
			// Drop the key and its value: every following line that is
			// indented deeper, and the blank lines inside a block scalar.
			for i+1 < len(lines) {
				next := lines[i+1]
				nextTrimmed := strings.TrimLeft(next, " ")
				if strings.TrimSpace(next) != "" && len(next)-len(nextTrimmed) <= indent {
					break
				}
				i++
			}
		case formatJSON:
			if !strings.HasPrefix(trimmed, `"`+corev1.LastAppliedConfigAnnotation+`":`) {
				kept = append(kept, line)
				continue
			}
			// The value is one JSON string on the same line. When it was the
			// last member of its object, the member before it must lose its
			// comma.
			if !strings.HasSuffix(strings.TrimRight(line, "\r\n"), ",") && len(kept) > 0 {
				previous := strings.TrimRight(kept[len(kept)-1], "\r\n")
				if strings.HasSuffix(previous, ",") {
					kept[len(kept)-1] = strings.TrimSuffix(previous, ",") + "\n"
				}
			}
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}

// kubectlStdout prepares kubectl's stdout for the caller: repeated lines of a
// log collapsed, a manifest without its last-applied copy, and the whole cut
// to limit -- a log from its end, since that is where it failed.
func kubectlStdout(args []string, stdout string, limit int) string {
	switch kubectlVerb(args) {
	case verbLogs:
		return limitOutputAt(collapseRepeats(stdout), limit, logsHeadPercent)
	case verbGet:
		if format := outputFormat(args); format == formatYAML || format == formatJSON {
			stdout = stripLastApplied(stdout, format)
		}
	}
	return limitOutput(stdout, limit)
}

// execResult renders a finished kubectl or helm run. stdout is the output
// already prepared for the caller.
//
// A successful run returns its stdout, followed by a [stderr] section when
// the tool warned about something. A failed run is an error result that
// starts with how it failed (exit code, or the timeout) and stderr, where the
// tool says why, followed by whatever it had printed to stdout.
func execResult(tool string, result *capi.ExecResult, stdout string, limit int) *mcp.CallToolResult {
	stderr := strings.TrimRight(result.Stderr, "\n")

	if !result.Failed() {
		var text strings.Builder
		text.WriteString(stdout)
		if stderr != "" {
			if stdout != "" && !strings.HasSuffix(stdout, "\n") {
				text.WriteString("\n")
			}
			text.WriteString("[stderr]\n")
			text.WriteString(limitOutput(stderr, stderrLimit))
			text.WriteString("\n")
		}
		if text.Len() == 0 {
			text.WriteString("(no output)\n")
		}
		return mcp.NewToolResultText(text.String())
	}

	var text strings.Builder
	if result.TimedOut {
		fmt.Fprintf(&text, "%s was stopped after %s without finishing. A command that follows or watches "+
			"(logs -f, get -w, port-forward) never finishes: drop that flag, or raise timeout_seconds for a command that is only slow.\n",
			tool, result.Timeout)
	} else {
		fmt.Fprintf(&text, "%s failed with exit code %d\n", tool, result.ExitCode)
	}
	if stderr != "" {
		text.WriteString(limitOutput(stderr, limit))
		text.WriteString("\n")
	}
	if stdout != "" {
		text.WriteString("[stdout]\n")
		text.WriteString(stdout)
	}
	return mcp.NewToolResultError(text.String())
}
