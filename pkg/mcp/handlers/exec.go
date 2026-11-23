package handlers

import (
	"fmt"
	"path"
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

// terminatedByRemote is the line kubectl exec writes to stderr when the
// command it ran in the container ended with status 1. Without it, a status
// of 1 is kubectl's own failure (no such pod, forbidden), not the command's.
const terminatedByRemote = "command terminated with exit code 1"

// grepNoMatchText ends capi_kubectl's answer for a kubectl exec whose command
// ends in grep and matched nothing. grep says "no line matched" with status 1
// and no output; reported as a failure, it reads as a broken command and gets
// retried, when it is the answer.
const grepNoMatchText = "(the last grep matched no lines: grep's status 1 means no match, not an error)\n"

// kubectlResult renders a finished kubectl run: execResult, except that a
// grep in the container that matched nothing is an answer, not a failure.
func kubectlResult(args []string, result *capi.ExecResult, stdout string, limit int) *mcp.CallToolResult {
	if !grepFoundNothing(args, result) {
		return execResult("kubectl", result, stdout, limit)
	}
	// What the commands before grep printed stays, the kubectl line goes.
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(result.Stderr, "\n"), "\n") {
		if strings.TrimSpace(line) != terminatedByRemote {
			kept = append(kept, line)
		}
	}
	text := successText(stdout, strings.TrimSpace(strings.Join(kept, "\n")))
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return mcp.NewToolResultText(text + grepNoMatchText)
}

// grepFoundNothing reports whether a finished kubectl run is a kubectl exec
// whose command in the container ends in grep, and grep matched no lines:
// the command ended with status 1 (kubectl's terminatedByRemote line).
//
// The command is grep itself, or `sh -c <script>` (bash, ash, dash, zsh)
// whose last command is a plain pipeline ending in grep; what came before it
// does not change the status. A script that joins its last command with &&
// or ||, sets errexit or pipefail, or uses command substitution is not read:
// any of them can end in status 1 without grep having run, and such a run
// stays a failure.
func grepFoundNothing(args []string, result *capi.ExecResult) bool {
	if result.TimedOut || result.ExitCode != 1 || kubectlVerb(args) != "exec" {
		return false
	}
	terminated := false
	for _, line := range strings.Split(result.Stderr, "\n") {
		terminated = terminated || strings.TrimSpace(line) == terminatedByRemote
	}
	if !terminated {
		return false
	}
	remote := remoteCommand(args)
	switch {
	case len(remote) == 0:
		return false
	case isGrep(remote[0]):
		return true
	case len(remote) >= 3 && isShell(remote[0]) && remote[1] == "-c":
		return endsInGrep(remote[2])
	}
	return false
}

// remoteCommand returns the command a kubectl exec runs in the container: the
// arguments after the first "--".
func remoteCommand(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func isGrep(word string) bool {
	switch path.Base(word) {
	case "grep", "egrep", "fgrep":
		return true
	}
	return false
}

func isShell(word string) bool {
	switch path.Base(word) {
	case "sh", "bash", "ash", "dash", "zsh":
		return true
	}
	return false
}

// endsInGrep reports whether a shell script's last command is a pipeline whose
// last stage is grep, so that the script's status is grep's. Separators inside
// quotes do not count.
func endsInGrep(script string) bool {
	if strings.Contains(script, "pipefail") || strings.Contains(script, "errexit") ||
		strings.Contains(script, "set -e") || strings.Contains(script, "$(") || strings.Contains(script, "`") {
		return false
	}
	script = strings.TrimRight(script, " \t\r\n;")
	// stage is where the last pipeline stage starts; joined says the last
	// command so far has a && or || in it.
	stage, joined := 0, false
	var quote byte
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\':
			i++
		case c == ';' || c == '\n':
			stage, joined = i+1, false
		case (c == '&' || c == '|') && i > 0 && (script[i-1] == '>' || script[i-1] == '<'):
			// A redirection (2>&1, >&2, >|), not a separator.
		case c == '&' && i+1 < len(script) && script[i+1] == '>':
			// &>file
		case (c == '&' || c == '|') && i+1 < len(script) && script[i+1] == c:
			joined = true
			i++
		case c == '|' && i+1 < len(script) && script[i+1] == '&':
			// |& pipes stderr as well.
			stage = i + 2
			i++
		case c == '&':
			stage, joined = i+1, false
		case c == '|':
			stage = i + 1
		}
	}
	if quote != 0 || joined {
		return false
	}
	words := strings.Fields(script[stage:])
	// Variable assignments before the command (LC_ALL=C grep ...).
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	return len(words) > 0 && isGrep(words[0])
}

// successText is a successful run's output: its stdout, followed by a
// [stderr] section when the tool warned about something; "" when it printed
// nothing at all.
func successText(stdout, stderr string) string {
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
	return text.String()
}

// execResult renders a finished kubectl or helm run. stdout is the output
// already prepared for the caller.
//
// A successful run returns successText. A failed run is an error result that
// starts with how it failed (exit code, or the timeout) and stderr, where the
// tool says why, followed by whatever it had printed to stdout.
func execResult(tool string, result *capi.ExecResult, stdout string, limit int) *mcp.CallToolResult {
	stderr := strings.TrimRight(result.Stderr, "\n")

	if !result.Failed() {
		text := successText(stdout, stderr)
		if text == "" {
			text = "(no output)\n"
		}
		return mcp.NewToolResultText(text)
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
