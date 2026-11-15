package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// resultText returns the text content of a tool result.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result.Content)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

func TestJSONResultIsTextOnly(t *testing.T) {
	result, err := jsonResult(map[string]any{"b": 1, "a": "x"})
	if err != nil {
		t.Fatalf("jsonResult: %v", err)
	}
	if result.StructuredContent != nil {
		t.Fatalf("structuredContent must stay unset, got %v", result.StructuredContent)
	}
	if got, want := resultText(t, result), `{"a":"x","b":1}`; got != want {
		t.Fatalf("text = %s, want %s", got, want)
	}
}

func TestListResultEncodesEmptySliceAsArray(t *testing.T) {
	result, err := listResult(make([]ClusterSummary, 0))
	if err != nil {
		t.Fatalf("listResult: %v", err)
	}
	if got, want := resultText(t, result), `{"items":[]}`; got != want {
		t.Fatalf("text = %s, want %s", got, want)
	}
}

func TestGetClusterResultInlinesSummaryOrCandidates(t *testing.T) {
	summary := ClusterSummary{Name: "c1", Namespace: "ns", Provider: "aws"}

	single, err := json.Marshal(getClusterResult{ClusterSummary: &summary})
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(single, &top); err != nil {
		t.Fatal(err)
	}
	if top["name"] != "c1" || top["namespace"] != "ns" {
		t.Fatalf("summary fields must be inlined at the top level, got %s", single)
	}
	if _, ok := top["candidates"]; ok {
		t.Fatalf("candidates must be omitted for a single match, got %s", single)
	}

	multi, err := json.Marshal(getClusterResult{MatchedBy: "labelValue", Candidates: []ClusterSummary{summary}})
	if err != nil {
		t.Fatal(err)
	}
	top = map[string]any{}
	if err := json.Unmarshal(multi, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["name"]; ok {
		t.Fatalf("no cluster fields expected at the top level for multiple matches, got %s", multi)
	}
	if candidates, ok := top["candidates"].([]any); !ok || len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %s", multi)
	}
}

func TestClusterSummaryDropsSystemLabelsAndNamesHealthyConditions(t *testing.T) {
	status := &capi.ClusterStatus{
		Name:      "c1",
		Namespace: "ns",
		Provider:  capi.ProviderAWS,
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name": "c1",
			"env":                           "prod",
		},
		Annotations: map[string]string{
			"cluster.x-k8s.io/conversion-data":  `{"apiVersion":"cluster.x-k8s.io/v1beta2"}`,
			"cluster.giantswarm.io/description": "prod MC",
		},
		Conditions: clusterv1.Conditions{
			{Type: clusterv1.ReadyCondition, Status: "True", Reason: "ClusterReady", Message: "all good"},
		},
	}

	got := clusterSummary(status)
	if _, ok := got.Labels["cluster.x-k8s.io/cluster-name"]; ok {
		t.Fatalf("system labels must be dropped, got %v", got.Labels)
	}
	if got.Labels["env"] != "prod" {
		t.Fatalf("user labels must be kept, got %v", got.Labels)
	}
	if _, ok := got.Annotations["cluster.x-k8s.io/conversion-data"]; ok {
		t.Fatalf("system annotations must be dropped, got %v", got.Annotations)
	}
	if got.Annotations["cluster.giantswarm.io/description"] != "prod MC" {
		t.Fatalf("user annotations must be kept, got %v", got.Annotations)
	}
	if len(got.Problems) != 0 || len(got.HealthyConditions) != 1 || got.HealthyConditions[0] != "Ready" {
		t.Fatalf("a True condition must be named as healthy and nothing else, got %+v", got.ConditionDigest)
	}
	if got.Provider != "aws" {
		t.Fatalf("provider = %q, want aws", got.Provider)
	}
}

func TestCAPIConditionsSplitByHealth(t *testing.T) {
	got := capiConditions(clusterv1.Conditions{
		{Type: "InfrastructureReady", Status: corev1.ConditionTrue, Reason: "Provisioned", Message: "noise"},
		{Type: "ControlPlaneReady", Status: corev1.ConditionFalse, Severity: clusterv1.ConditionSeverityError, Reason: "ScalingUp", Message: "1 of 3 replicas"},
		{Type: "TopologyReconciled", Status: corev1.ConditionUnknown},
	})

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"problems":[` +
		`{"type":"ControlPlaneReady","status":"False","severity":"Error","reason":"ScalingUp","message":"1 of 3 replicas"},` +
		`{"type":"TopologyReconciled","status":"Unknown"}],` +
		`"healthyConditions":["InfrastructureReady"]}`
	if string(body) != want {
		t.Fatalf("digest = %s\nwant     %s", body, want)
	}

	if body, _ := json.Marshal(capiConditions(nil)); string(body) != `{}` {
		t.Fatalf("no conditions must encode as no fields, got %s", body)
	}
}

// A node's pressure conditions are healthy when False, the opposite of Ready.
func TestNodeConditionsKnowTheirPolarity(t *testing.T) {
	got := nodeConditions([]corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady", Message: "kubelet is posting ready status"},
		{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse, Reason: "KubeletHasSufficientMemory"},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasDiskPressure", Message: "kubelet has disk pressure"},
		{Type: corev1.NodeNetworkUnavailable, Status: corev1.ConditionUnknown},
	})

	if want := []string{"Ready=True", "MemoryPressure=False"}; !slices.Equal(got.HealthyConditions, want) {
		t.Fatalf("healthy = %v, want %v", got.HealthyConditions, want)
	}
	if len(got.Problems) != 2 || got.Problems[0].Type != "DiskPressure" || got.Problems[0].Message != "kubelet has disk pressure" ||
		got.Problems[1].Type != "NetworkUnavailable" {
		t.Fatalf("problems = %+v, want DiskPressure with its message, then NetworkUnavailable", got.Problems)
	}

	notReady := nodeConditions([]corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}})
	if len(notReady.Problems) != 1 || len(notReady.HealthyConditions) != 0 {
		t.Fatalf("a node that is not Ready must be a problem, got %+v", notReady)
	}
}

func TestClusterListItemKeepsProblemsAndLabelsOnly(t *testing.T) {
	status := &capi.ClusterStatus{
		Name:        "c1",
		Namespace:   "ns",
		Labels:      map[string]string{"env": "prod"},
		Annotations: map[string]string{"owner": "team-a"},
		Conditions: clusterv1.Conditions{
			{Type: "InfrastructureReady", Status: corev1.ConditionTrue},
			{Type: "ControlPlaneReady", Status: corev1.ConditionFalse, Reason: "ScalingUp"},
		},
	}

	got := clusterListItem(status)
	if got.Annotations != nil || got.HealthyConditions != nil {
		t.Fatalf("a list item must leave out annotations and healthy conditions, got %+v", got)
	}
	if got.Labels["env"] != "prod" {
		t.Fatalf("a list item must keep labels, got %v", got.Labels)
	}
	if len(got.Problems) != 1 || got.Problems[0].Reason != "ScalingUp" {
		t.Fatalf("a list item must keep its problems, got %+v", got.Problems)
	}
}

func TestLimitOutput(t *testing.T) {
	t.Run("short output is returned as it is", func(t *testing.T) {
		if got := limitOutput("NAME\npod-1\n", 100); got != "NAME\npod-1\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("long output keeps whole lines of its start and end", func(t *testing.T) {
		var lines []string
		for i := range 100 {
			lines = append(lines, fmt.Sprintf("line-%03d", i))
		}
		output := strings.Join(lines, "\n") + "\n"

		got := limitOutput(output, 200)
		if !strings.HasPrefix(got, "line-000\n") || !strings.HasSuffix(got, "line-099\n") {
			t.Fatalf("start and end must survive, got %q", got)
		}
		kept := 0
		for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "[output truncated: "):
				if !strings.Contains(line, fmt.Sprintf("of %d bytes", len(output))) || !strings.Contains(line, "max_output_bytes") {
					t.Fatalf("the marker must give the full size and the way out, got %q", line)
				}
			case len(line) == len("line-000") && strings.HasPrefix(line, "line-"):
				kept++
			default:
				t.Fatalf("a line was cut in two: %q", line)
			}
		}
		omitted := 100 - kept
		if !strings.Contains(got, fmt.Sprintf("(%d lines) omitted", omitted)) || omitted < 70 {
			t.Fatalf("kept %d lines, marker must count the %d omitted: %q", kept, omitted, got)
		}
		if len(got) > 200+250 {
			t.Fatalf("result is %d bytes, far over the limit", len(got))
		}
	})

	t.Run("output without line breaks is still cut, on valid UTF-8", func(t *testing.T) {
		got := limitOutput(strings.Repeat("é", 500), 101)
		if !utf8.ValidString(got) || !strings.Contains(got, "[output truncated: ") || len(got) > 101+250 {
			t.Fatalf("got %d bytes, valid=%v: %q", len(got), utf8.ValidString(got), got)
		}
	})
}

func TestOutputLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		arguments map[string]any
		want      int
		wantErr   bool
	}{
		"absent means the default":     {map[string]any{}, defaultMaxOutputBytes, false},
		"zero means the default":       {map[string]any{"max_output_bytes": float64(0)}, defaultMaxOutputBytes, false},
		"a number is taken":            {map[string]any{"max_output_bytes": float64(500)}, 500, false},
		"the maximum is allowed":       {map[string]any{"max_output_bytes": float64(maxMaxOutputBytes)}, maxMaxOutputBytes, false},
		"over the maximum is an error": {map[string]any{"max_output_bytes": float64(maxMaxOutputBytes + 1)}, 0, true},
		"negative is an error":         {map[string]any{"max_output_bytes": float64(-1)}, 0, true},
		"a fraction is an error":       {map[string]any{"max_output_bytes": 1.5}, 0, true},
		"a string is an error":         {map[string]any{"max_output_bytes": "500"}, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := outputLimit(tc.arguments)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("outputLimit() = %d, %v; want %d, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
