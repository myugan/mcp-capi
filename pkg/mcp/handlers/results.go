package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// Every tool returns its result as one JSON document in the text content.
// Aggregators (muster), UIs and LLM clients parse it; nothing has to scrape
// prose. List tools follow the mcp-toolkit paginated shape {items, nextCursor}
// (https://github.com/giantswarm/mcp-toolkit/blob/main/docs/conventions.md);
// nextCursor is omitted because every list tool still returns all matches in
// one page -- server-side pagination is a follow-up.
//
// structuredContent is deliberately left unset. muster mirrors it both
// natively and inside its call_tool envelope, so a payload placed there
// travels twice; the text content is the one carrier every client reads.

// jsonResult encodes v as the JSON text content of a successful tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool result: %w", err)
	}
	return mcp.NewToolResultText(string(body)), nil
}

// ListResult is the shape of every list tool's result.
type ListResult struct {
	Items      any        `json:"items"`
	NextCursor mcp.Cursor `json:"nextCursor,omitempty"`
}

// listResult wraps items in the {items} envelope. Callers pass a non-nil
// slice so an empty list encodes as [] rather than null.
func listResult(items any) (*mcp.CallToolResult, error) {
	return jsonResult(ListResult{Items: items})
}

// ObjectRef names a related Kubernetes object.
type ObjectRef struct {
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// Condition is one condition that is not in its healthy state, with what the
// controller said about it.
type Condition struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Severity string `json:"severity,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ConditionDigest splits an object's conditions by health, so a reader sees
// what is wrong without scanning what is fine. Problems carries every
// condition that is not in its healthy state, in full. HealthyConditions
// names the rest: their reason and message only restate the type ("kubelet
// has sufficient memory available") and are left out.
//
// Both fields are omitted when empty, so an object without a problems field
// has no unhealthy condition.
type ConditionDigest struct {
	Problems          []Condition `json:"problems,omitempty"`
	HealthyConditions []string    `json:"healthyConditions,omitempty"`
}

// problemsOnly returns the digest without the healthy condition names, for
// list items: a list is scanned for what is wrong, and one object's detail
// tool names the healthy conditions.
func (d ConditionDigest) problemsOnly() ConditionDigest {
	return ConditionDigest{Problems: d.Problems}
}

// Address is a machine or node address.
type Address struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// ClusterSummary is the digest of a CAPI Cluster returned by the cluster tools.
type ClusterSummary struct {
	Name                string            `json:"name"`
	Namespace           string            `json:"namespace"`
	Phase               string            `json:"phase,omitempty"`
	Ready               bool              `json:"ready"`
	Paused              bool              `json:"paused,omitempty"`
	ControlPlaneReady   bool              `json:"controlPlaneReady"`
	InfrastructureReady bool              `json:"infrastructureReady"`
	Provider            string            `json:"provider,omitempty"`
	Version             string            `json:"version,omitempty"`
	TotalMachines       int               `json:"totalMachines"`
	ReadyMachines       int               `json:"readyMachines"`
	Labels              map[string]string `json:"labels,omitempty"`
	Annotations         map[string]string `json:"annotations,omitempty"`
	ConditionDigest
}

// clusterSummary converts a capi.ClusterStatus into its JSON digest. Labels
// and annotations under the cluster.x-k8s.io domain are system-managed and
// left out.
func clusterSummary(status *capi.ClusterStatus) ClusterSummary {
	return ClusterSummary{
		Name:                status.Name,
		Namespace:           status.Namespace,
		Phase:               status.Phase,
		Ready:               status.Ready,
		Paused:              status.Paused,
		ControlPlaneReady:   status.ControlPlaneReady,
		InfrastructureReady: status.InfraReady,
		Provider:            string(status.Provider),
		Version:             status.Version,
		TotalMachines:       status.TotalMachines,
		ReadyMachines:       status.ReadyMachines,
		Labels:              capi.UserMetadata(status.Labels),
		Annotations:         capi.UserMetadata(status.Annotations),
		ConditionDigest:     capiConditions(status.Conditions),
	}
}

// clusterListItem is the ClusterSummary of a cluster listed among others:
// without annotations and healthy condition names, which capi_get_cluster and
// capi_cluster_status return for one cluster. Labels stay, the list tools
// filter on them.
func clusterListItem(status *capi.ClusterStatus) ClusterSummary {
	summary := clusterSummary(status)
	summary.Annotations = nil
	summary.ConditionDigest = summary.problemsOnly()
	return summary
}

// capiConditions digests CAPI conditions. Every v1beta1 CAPI condition is
// healthy when True, so anything else (False or Unknown) is a problem.
func capiConditions(conditions clusterv1.Conditions) ConditionDigest {
	var digest ConditionDigest
	for _, c := range conditions {
		if c.Status == corev1.ConditionTrue {
			digest.HealthyConditions = append(digest.HealthyConditions, string(c.Type))
			continue
		}
		digest.Problems = append(digest.Problems, Condition{
			Type:     string(c.Type),
			Status:   string(c.Status),
			Severity: string(c.Severity),
			Reason:   c.Reason,
			Message:  c.Message,
		})
	}
	return digest
}

// nodeConditions digests core Node conditions. Ready is healthy when True;
// every other node condition (MemoryPressure, DiskPressure, PIDPressure,
// NetworkUnavailable and those node-problem-detector adds) reports a fault
// and is healthy when False. Because the healthy status differs by type, a
// healthy node condition is named with it ("MemoryPressure=False"), so the
// name alone is never read as the fault being present.
func nodeConditions(conditions []corev1.NodeCondition) ConditionDigest {
	var digest ConditionDigest
	for _, c := range conditions {
		healthy := corev1.ConditionFalse
		if c.Type == corev1.NodeReady {
			healthy = corev1.ConditionTrue
		}
		if c.Status == healthy {
			digest.HealthyConditions = append(digest.HealthyConditions, fmt.Sprintf("%s=%s", c.Type, c.Status))
			continue
		}
		digest.Problems = append(digest.Problems, Condition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	return digest
}

// defaultMaxOutputBytes caps what capi_kubectl and capi_helm return when the
// caller sets no max_output_bytes: roughly 6000 tokens, enough for a listing
// of a few hundred objects or one object's manifest, far below a log dump.
const defaultMaxOutputBytes = 24000

// maxOutputBytesDescription documents the max_output_bytes parameter.
const maxOutputBytesDescription = "Output size limit in bytes (default 24000). Longer output keeps its start and end " +
	"and says how much was cut from the middle"

// maxMaxOutputBytes is the highest max_output_bytes a caller may ask for.
const maxMaxOutputBytes = 1000000

// outputLimit reads the max_output_bytes argument: absent or zero means the
// default, anything else must be a whole number up to maxMaxOutputBytes.
func outputLimit(arguments map[string]any) (int, error) {
	raw, ok := arguments["max_output_bytes"]
	if !ok || raw == nil {
		return defaultMaxOutputBytes, nil
	}
	n, ok := raw.(float64)
	if !ok || n < 0 || n > maxMaxOutputBytes || n != float64(int(n)) {
		return 0, fmt.Errorf("max_output_bytes must be a whole number from 1 to %d", maxMaxOutputBytes)
	}
	if n == 0 {
		return defaultMaxOutputBytes, nil
	}
	return int(n), nil
}

// limitOutput returns output unchanged when it fits in limit bytes. Otherwise
// it keeps the start and the end -- a table's header and first rows, and the
// last lines, where a command prints its error -- and replaces the middle
// with one line that says how much is missing and how to get it. Cuts fall on
// line boundaries when the output has lines to cut on.
func limitOutput(output string, limit int) string {
	if limit <= 0 || len(output) <= limit {
		return output
	}

	half := limit / 2
	head := output[:half]
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	}
	tail := output[len(output)-half:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	// A cut that is not on a line boundary may split a UTF-8 sequence.
	head = strings.ToValidUTF8(head, "")
	tail = strings.ToValidUTF8(tail, "")

	omitted := len(output) - len(head) - len(tail)
	omittedLines := strings.Count(output[len(head):len(output)-len(tail)], "\n")
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	return head + fmt.Sprintf("[output truncated: %d of %d bytes (%d lines) omitted here. Narrow the command "+
		"(a namespace, a label selector, --tail, --since, -o jsonpath or custom-columns) or raise max_output_bytes.]\n",
		omitted, len(output), omittedLines) + tail
}

// machineAddresses converts CAPI machine addresses into their digest.
func machineAddresses(addresses clusterv1.MachineAddresses) []Address {
	if len(addresses) == 0 {
		return nil
	}
	out := make([]Address, 0, len(addresses))
	for _, a := range addresses {
		out = append(out, Address{Type: string(a.Type), Address: a.Address})
	}
	return out
}

// nodeAddresses converts core Node addresses into their digest.
func nodeAddresses(addresses []corev1.NodeAddress) []Address {
	if len(addresses) == 0 {
		return nil
	}
	out := make([]Address, 0, len(addresses))
	for _, a := range addresses {
		out = append(out, Address{Type: string(a.Type), Address: a.Address})
	}
	return out
}

// objectRef converts a core ObjectReference into its digest; nil in, nil out.
func objectRef(ref *corev1.ObjectReference) *ObjectRef {
	if ref == nil {
		return nil
	}
	return &ObjectRef{Kind: ref.Kind, Name: ref.Name, APIVersion: ref.APIVersion}
}

// machineReady reports whether the machine carries a Ready condition set to True.
func machineReady(machine *clusterv1.Machine) bool {
	for _, condition := range machine.Status.Conditions {
		if condition.Type == clusterv1.ReadyCondition && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
