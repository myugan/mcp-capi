package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"sigs.k8s.io/yaml"

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// anyResultText returns the text of a result, error or not.
func anyResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected one content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

func TestKubectlVerb(t *testing.T) {
	for want, args := range map[string][]string{
		"get":  {"get", "pods", "-A"},
		"logs": {"-n", "kube-system", "logs", "coredns-1"},
		"top":  {"--namespace", "logs", "top", "pods"},
		"":     {"--help"},
	} {
		if got := kubectlVerb(args); got != want {
			t.Errorf("kubectlVerb(%v) = %q, want %q", args, got, want)
		}
	}
}

func TestOutputFormat(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "pod", "x", "-o", "yaml"}, "yaml"},
		{[]string{"get", "pod", "x", "-oyaml"}, "yaml"},
		{[]string{"get", "pod", "x", "-o=json"}, "json"},
		{[]string{"get", "pod", "x", "--output", "json"}, "json"},
		{[]string{"get", "pod", "x", "--output=wide"}, "wide"},
		{[]string{"get", "pods", "--only-one"}, ""},
		{[]string{"get", "pods"}, ""},
	} {
		if got := outputFormat(tc.args); got != tc.want {
			t.Errorf("outputFormat(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestCollapseRepeats(t *testing.T) {
	got := collapseRepeats("starting\n" + strings.Repeat("connection refused\n", 40) + "giving up\ngiving up\n\n\n\n\ndone")
	want := "starting\nconnection refused\n[previous line repeated 39 more times]\ngiving up\ngiving up\n\n\n\n\ndone"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	for _, unchanged := range []string{"", "a\nb\nc\n", "a\na\n", "no newline"} {
		if got := collapseRepeats(unchanged); got != unchanged {
			t.Errorf("collapseRepeats(%q) = %q, must be unchanged", unchanged, got)
		}
	}
}

const yamlWithLastApplied = `apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    deployment.kubernetes.io/revision: "3"
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web"},"spec":{"replicas":2}}

  creationTimestamp: "2026-11-01T10:00:00Z"
  name: web
spec:
  replicas: 2
`

func TestStripLastAppliedYAML(t *testing.T) {
	got := stripLastApplied(yamlWithLastApplied, formatYAML)
	if strings.Contains(got, "last-applied-configuration") || strings.Contains(got, `"apiVersion":"apps/v1"`) {
		t.Fatalf("the annotation and its value must be gone:\n%s", got)
	}

	var object map[string]any
	if err := yaml.Unmarshal([]byte(got), &object); err != nil {
		t.Fatalf("result is not valid YAML: %v\n%s", err, got)
	}
	metadata := object["metadata"].(map[string]any)
	if metadata["name"] != "web" || metadata["creationTimestamp"] != "2026-11-01T10:00:00Z" {
		t.Fatalf("the fields after the annotation must survive, got %v", metadata)
	}
	if revision := metadata["annotations"].(map[string]any)["deployment.kubernetes.io/revision"]; revision != "3" {
		t.Fatalf("the other annotations must survive, got %v", metadata["annotations"])
	}
	if object["spec"].(map[string]any)["replicas"] != float64(2) {
		t.Fatalf("spec must survive, got %v", object["spec"])
	}

	// The only annotation: the annotations key is left with no value.
	only := "metadata:\n  annotations:\n    kubectl.kubernetes.io/last-applied-configuration: |\n      {}\n  name: web\n"
	if got := stripLastApplied(only, formatYAML); got != "metadata:\n  annotations:\n  name: web\n" {
		t.Fatalf("got %q", got)
	}
}

func TestStripLastAppliedJSON(t *testing.T) {
	lastApplied := `"kubectl.kubernetes.io/last-applied-configuration": "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\"}\n"`
	for name, annotations := range map[string]string{
		"last member":   "            \"owner\": \"team-a\",\n            " + lastApplied + "\n",
		"first member":  "            " + lastApplied + ",\n            \"owner\": \"team-a\"\n",
		"only member":   "            " + lastApplied + "\n",
		"middle member": "            \"a\": \"1\",\n            " + lastApplied + ",\n            \"owner\": \"team-a\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			document := "{\n    \"apiVersion\": \"v1\",\n    \"metadata\": {\n        \"annotations\": {\n" + annotations +
				"        },\n        \"name\": \"web\"\n    }\n}\n"

			got := stripLastApplied(document, formatJSON)
			if strings.Contains(got, "last-applied-configuration") {
				t.Fatalf("the annotation must be gone:\n%s", got)
			}
			var object map[string]any
			if err := json.Unmarshal([]byte(got), &object); err != nil {
				t.Fatalf("result is not valid JSON: %v\n%s", err, got)
			}
			metadata := object["metadata"].(map[string]any)
			if metadata["name"] != "web" {
				t.Fatalf("name must survive, got %v", metadata)
			}
			owner, hasOwner := metadata["annotations"].(map[string]any)["owner"]
			if strings.Contains(annotations, "owner") != hasOwner || (hasOwner && owner != "team-a") {
				t.Fatalf("the other annotations must survive, got %v", metadata["annotations"])
			}
		})
	}
}

func TestKubectlStdoutShapesByCommand(t *testing.T) {
	t.Run("a manifest loses its last-applied copy only for get -o yaml or json", func(t *testing.T) {
		if got := kubectlStdout([]string{"get", "deploy", "web", "-o", "yaml"}, yamlWithLastApplied, 100000); strings.Contains(got, "last-applied") {
			t.Fatalf("get -o yaml must strip it:\n%s", got)
		}
		for _, args := range [][]string{
			{"get", "deploy", "web", "-o", "jsonpath={.metadata.annotations}"},
			{"apply", "view-last-applied", "deploy/web", "-o", "yaml"},
			{"describe", "deploy", "web"},
		} {
			if got := kubectlStdout(args, yamlWithLastApplied, 100000); got != yamlWithLastApplied {
				t.Errorf("%v must be returned as printed", args)
			}
		}
	})

	t.Run("a log keeps mostly its end", func(t *testing.T) {
		var lines []string
		for i := range 400 {
			lines = append(lines, fmt.Sprintf("log-line-%04d", i))
		}
		log := strings.Join(lines, "\n") + "\n"

		got := kubectlStdout([]string{"logs", "web-1"}, log, 1000)
		head, tail, cut := strings.Cut(got, "[output truncated: ")
		if !cut || !strings.HasPrefix(got, "log-line-0000\n") || !strings.HasSuffix(got, "log-line-0399\n") {
			t.Fatalf("a long log must be cut in the middle and keep both ends, got %q", got)
		}
		if len(tail) < 3*len(head) {
			t.Fatalf("the end of a log must get most of the room: head %d bytes, tail %d", len(head), len(tail))
		}
	})

	t.Run("a log's repeated lines are counted", func(t *testing.T) {
		got := kubectlStdout([]string{"-n", "prod", "logs", "web-1"}, strings.Repeat("panic: nil map\n", 500), 100000)
		if got != "panic: nil map\n[previous line repeated 499 more times]\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("identical lines outside a log are left alone", func(t *testing.T) {
		yamlList := "items:\n- \"\"\n- \"\"\n- \"\"\n- \"\"\n"
		if got := kubectlStdout([]string{"get", "cm", "x", "-o", "yaml"}, yamlList, 100000); got != yamlList {
			t.Fatalf("got %q", got)
		}
	})
}

func TestExecResultRendering(t *testing.T) {
	t.Run("success is the stdout and nothing else", func(t *testing.T) {
		result := execResult("kubectl", &capi.ExecResult{Stdout: "NAME\nweb-1\n"}, "NAME\nweb-1\n", 1000)
		if result.IsError || anyResultText(t, result) != "NAME\nweb-1\n" {
			t.Fatalf("got error=%v %q", result.IsError, anyResultText(t, result))
		}
	})

	t.Run("warnings follow the output, marked", func(t *testing.T) {
		result := execResult("kubectl", &capi.ExecResult{Stdout: "{}", Stderr: "Warning: deprecated\n"}, "{}", 1000)
		if got, want := anyResultText(t, result), "{}\n[stderr]\nWarning: deprecated\n"; result.IsError || got != want {
			t.Fatalf("got error=%v %q, want %q", result.IsError, got, want)
		}
	})

	t.Run("stderr alone is shown, not an empty result", func(t *testing.T) {
		result := execResult("kubectl", &capi.ExecResult{Stderr: "No resources found in default namespace.\n"}, "", 1000)
		if got, want := anyResultText(t, result), "[stderr]\nNo resources found in default namespace.\n"; result.IsError || got != want {
			t.Fatalf("got error=%v %q, want %q", result.IsError, got, want)
		}
	})

	t.Run("no output at all says so", func(t *testing.T) {
		result := execResult("helm", &capi.ExecResult{}, "", 1000)
		if got := anyResultText(t, result); result.IsError || got != "(no output)\n" {
			t.Fatalf("got error=%v %q", result.IsError, got)
		}
	})

	t.Run("a failure leads with the exit code and stderr", func(t *testing.T) {
		result := execResult("kubectl", &capi.ExecResult{
			ExitCode: 1,
			Stdout:   "pod/a created\n",
			Stderr:   "Error from server (Forbidden): pods is forbidden\n",
		}, "pod/a created\n", 1000)
		want := "kubectl failed with exit code 1\nError from server (Forbidden): pods is forbidden\n[stdout]\npod/a created\n"
		if got := anyResultText(t, result); !result.IsError || got != want {
			t.Fatalf("got error=%v %q\nwant %q", result.IsError, got, want)
		}
	})

	t.Run("a timeout says so and keeps what was printed", func(t *testing.T) {
		result := execResult("kubectl", &capi.ExecResult{
			TimedOut: true, ExitCode: -1, Timeout: 2 * time.Minute, Stdout: "first log line\n",
		}, "first log line\n", 1000)
		got := anyResultText(t, result)
		if !result.IsError || !strings.HasPrefix(got, "kubectl was stopped after 2m0s without finishing.") ||
			!strings.Contains(got, "timeout_seconds") || !strings.HasSuffix(got, "[stdout]\nfirst log line\n") {
			t.Fatalf("got error=%v %q", result.IsError, got)
		}
	})
}

func TestExecArguments(t *testing.T) {
	args, opts, limit, err := execArguments(map[string]any{
		"args": []any{"get", "pods"}, "stdin": "x", "timeout_seconds": float64(300), "max_output_bytes": float64(500),
	})
	if err != nil || len(args) != 2 || opts.Stdin != "x" || opts.Timeout != 5*time.Minute || limit != 500 {
		t.Fatalf("got %v %+v %d %v", args, opts, limit, err)
	}

	_, opts, limit, err = execArguments(map[string]any{"args": []any{"list"}})
	if err != nil || opts.Timeout != 0 || limit != defaultMaxOutputBytes {
		t.Fatalf("defaults: got %+v %d %v", opts, limit, err)
	}

	for name, arguments := range map[string]map[string]any{
		"no args":             {},
		"empty args":          {"args": []any{}},
		"an arg not a string": {"args": []any{"get", 1.0}},
		"timeout too long":    {"args": []any{"get"}, "timeout_seconds": float64(601)},
		"timeout negative":    {"args": []any{"get"}, "timeout_seconds": float64(-1)},
		"timeout a string":    {"args": []any{"get"}, "timeout_seconds": "60"},
		"limit too large":     {"args": []any{"get"}, "max_output_bytes": float64(maxMaxOutputBytes + 1)},
	} {
		if _, _, _, err := execArguments(arguments); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
