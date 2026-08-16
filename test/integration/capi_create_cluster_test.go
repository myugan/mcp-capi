package integration_test

import (
	"testing"

	"github.com/giantswarm/mcp-capi/test/harness"
)

func TestCapiCreateCluster(t *testing.T) {
	t.Parallel()

	t.Run("returns error without namespace argument", func(t *testing.T) {
		t.Parallel()

		harness.New(t).
			ToolCall("capi_create_cluster").
			WithArg("name", "some-cluster").
			WithArg("cluster_class", "capn-audit").
			WithArg("kubernetes_version", "v1.35.0").
			AssertError("missing_namespace.golden").
			Execute()
	})

	t.Run("returns error for non-existent cluster class", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster").
			WithArg("name", "some-cluster").
			WithArg("namespace", namespace).
			WithArg("cluster_class", "does-not-exist").
			WithArg("kubernetes_version", "v1.35.0").
			AssertError("missing_cluster_class.golden").
			Execute()
	})

	t.Run("creates a basic cluster", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			CreateClusterClass(namespace, "capn-audit").
			ToolCall("capi_create_cluster").
			WithArg("name", "basic-cluster").
			WithArg("namespace", namespace).
			WithArg("cluster_class", "capn-audit").
			WithArg("kubernetes_version", "v1.35.0").
			AssertContent("basic.golden").
			Execute()
	})

	t.Run("creates a cluster with explicit cluster network", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			CreateClusterClass(namespace, "capn-audit").
			ToolCall("capi_create_cluster").
			WithArg("name", "timbernetes").
			WithArg("namespace", namespace).
			WithArg("cluster_class", "capn-audit").
			WithArg("kubernetes_version", "v1.35.0").
			WithArg("control_plane_replicas", 1).
			WithArg("cluster_network", map[string]any{
				"pods":           []any{"10.244.0.0/16"},
				"services":       []any{"10.96.0.0/12"},
				"service_domain": "cluster.local",
			}).
			AssertContent("with_cluster_network.golden").
			Execute()
	})

	t.Run("creates a cluster with per-machine-deployment variable overrides", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			CreateClusterClass(namespace, "capn-audit").
			ToolCall("capi_create_cluster").
			WithArg("name", "overridden-worker-cluster").
			WithArg("namespace", namespace).
			WithArg("cluster_class", "capn-audit").
			WithArg("kubernetes_version", "v1.35.0").
			WithArg("machine_deployments", []any{
				map[string]any{
					"class":    "default-worker",
					"name":     "md-0",
					"replicas": 2,
					"variables": map[string]any{
						"instance": map[string]any{
							"flavor": "c4-m8",
							"type":   "container",
						},
					},
				},
				map[string]any{
					"class":    "default-worker",
					"name":     "md-1",
					"replicas": 1,
				},
			}).
			AssertContent("with_md_variable_overrides.golden").
			Execute()
	})

	t.Run("returns error when a machine_deployments entry is missing name", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			CreateClusterClass(namespace, "capn-audit").
			ToolCall("capi_create_cluster").
			WithArg("name", "bad-md-cluster").
			WithArg("namespace", namespace).
			WithArg("cluster_class", "capn-audit").
			WithArg("kubernetes_version", "v1.35.0").
			WithArg("machine_deployments", []any{
				map[string]any{"class": "default-worker"},
			}).
			AssertError("missing_md_fields.golden").
			Execute()
	})
}
