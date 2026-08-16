package integration_test

import (
	"testing"

	"github.com/giantswarm/mcp-capi/test/harness"
)

func TestCapiCreateClusterResourceSet(t *testing.T) {
	t.Parallel()

	t.Run("returns error without namespace argument", func(t *testing.T) {
		t.Parallel()

		harness.New(t).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "some-crs").
			AssertError("missing_namespace.golden").
			Execute()
	})

	t.Run("returns error without cluster_selector argument", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "some-crs").
			WithArg("namespace", namespace).
			AssertError("missing_cluster_selector.golden").
			Execute()
	})

	t.Run("returns error without resources argument", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "some-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			AssertError("missing_resources.golden").
			Execute()
	})

	t.Run("returns error for invalid resource kind", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "some-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{"kind": "Deployment", "name": "not-supported"},
			}).
			AssertError("invalid_kind.golden").
			Execute()
	})

	t.Run("returns error for invalid strategy", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "some-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{"kind": "ConfigMap", "name": "cni-manifest"},
			}).
			WithArg("strategy", "Sometimes").
			AssertError("invalid_strategy.golden").
			Execute()
	})

	t.Run("creates a cluster resource set with an inline configmap", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "flannel").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"capn.cluster.x-k8s.io/deploy-kube-flannel": "true"}).
			WithArg("resources", []any{
				map[string]any{
					"kind": "ConfigMap",
					"name": "flannel-manifest",
					"data": map[string]any{"cni.yaml": "kind: ConfigMap\n"},
				},
			}).
			AssertContent("created_with_inline_configmap.golden").
			Execute()
	})

	t.Run("creates a cluster resource set referencing an existing secret", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			CreateSecret(namespace, "existing-secret", map[string][]byte{"cni.yaml": []byte("kind: Secret\n")}).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "existing-secret-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{"kind": "Secret", "name": "existing-secret"},
			}).
			AssertContent("created_referencing_existing_secret.golden").
			Execute()
	})

	t.Run("creates a cluster resource set with Reconcile strategy", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "reconciling-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{
					"kind": "ConfigMap",
					"name": "reconciling-manifest",
					"data": map[string]any{"cni.yaml": "kind: ConfigMap\n"},
				},
			}).
			WithArg("strategy", "Reconcile").
			AssertContent("created_with_reconcile_strategy.golden").
			Execute()
	})
}

func TestCapiListClusterResourceSets(t *testing.T) {
	t.Parallel()

	t.Run("lists no cluster resource sets in an empty namespace", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_list_cluster_resource_sets").
			WithArg("namespace", namespace).
			AssertContent("empty.golden").
			Execute()
	})

	t.Run("lists cluster resource sets after creating one", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "listed-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{
					"kind": "ConfigMap",
					"name": "listed-manifest",
					"data": map[string]any{"cni.yaml": "kind: ConfigMap\n"},
				},
			}).
			AssertContent("created_for_list.golden").
			ToolCall("capi_list_cluster_resource_sets").
			WithArg("namespace", namespace).
			AssertContent("with_one_entry.golden").
			Execute()
	})
}

func TestCapiGetClusterResourceSet(t *testing.T) {
	t.Parallel()

	t.Run("returns error for non-existent cluster resource set", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_get_cluster_resource_set").
			WithArg("namespace", namespace).
			WithArg("name", "does-not-exist").
			AssertError("not_found.golden").
			Execute()
	})

	t.Run("gets a cluster resource set", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "got-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{
					"kind": "ConfigMap",
					"name": "got-manifest",
					"data": map[string]any{"cni.yaml": "kind: ConfigMap\n"},
				},
			}).
			AssertContent("created_for_get.golden").
			ToolCall("capi_get_cluster_resource_set").
			WithArg("namespace", namespace).
			WithArg("name", "got-crs").
			AssertContent("basic.golden").
			Execute()
	})
}

func TestCapiDeleteClusterResourceSet(t *testing.T) {
	t.Parallel()

	t.Run("returns error for non-existent cluster resource set", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_delete_cluster_resource_set").
			WithArg("namespace", namespace).
			WithArg("name", "does-not-exist").
			AssertError("not_found.golden").
			Execute()
	})

	t.Run("deletes a cluster resource set", func(t *testing.T) {
		t.Parallel()
		namespace := testNamespace

		harness.New(t).
			CreateNamespace(namespace).
			ToolCall("capi_create_cluster_resource_set").
			WithArg("name", "deleted-crs").
			WithArg("namespace", namespace).
			WithArg("cluster_selector", map[string]any{"deploy-cni": "true"}).
			WithArg("resources", []any{
				map[string]any{
					"kind": "ConfigMap",
					"name": "deleted-manifest",
					"data": map[string]any{"cni.yaml": "kind: ConfigMap\n"},
				},
			}).
			AssertContent("created_for_delete.golden").
			ToolCall("capi_delete_cluster_resource_set").
			WithArg("namespace", namespace).
			WithArg("name", "deleted-crs").
			AssertContent("success.golden").
			ToolCall("capi_get_cluster_resource_set").
			WithArg("namespace", namespace).
			WithArg("name", "deleted-crs").
			AssertError("not_found_after_delete.golden").
			Execute()
	})
}
