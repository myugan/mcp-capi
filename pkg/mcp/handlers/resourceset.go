package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// buildResourceSetTools constructs and returns all ClusterResourceSet tools.
// ClusterResourceSets are the mechanism CAPI uses to apply arbitrary
// manifests (most commonly a CNI) to every Cluster matching a label
// selector -- e.g. the flannel ConfigMap + ClusterResourceSet pair deployed
// alongside every ClusterClass-based cluster in this environment.
func buildResourceSetTools(serverCtx *ServerContext) []ToolRegistration {
	var tools []ToolRegistration

	// capi_create_cluster_resource_set
	tools = append(tools, ToolRegistration{
		Tool: mcp.NewTool(
			"capi_create_cluster_resource_set",
			mcp.WithDescription("Create a ClusterResourceSet to apply manifests (e.g. a CNI) to every Cluster matching a label selector. "+
				"Each entry in resources is a Secret or ConfigMap holding one or more manifests: pass 'data' to create that Secret/ConfigMap "+
				"as part of this call, or omit 'data' to reference one that already exists. Does not create or label the target Cluster(s) -- "+
				"use capi_update_cluster to set the matching label on a Cluster."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Name of the ClusterResourceSet")),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace for the ClusterResourceSet (and any resources created alongside it)")),
			mcp.WithObject("cluster_selector", mcp.Required(),
				mcp.Description("Label key-value pairs a Cluster must have to receive these resources, e.g. {\"capn.cluster.x-k8s.io/deploy-kube-flannel\": \"true\"}")),
			mcp.WithArray("resources", mcp.Required(),
				mcp.Description("Resources to apply, e.g. [{\"kind\": \"ConfigMap\", \"name\": \"timbernetes-kube-flannel\", \"data\": {\"cni.yaml\": \"<manifest YAML>\"}}]"),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithString("strategy", mcp.Description("ApplyOnce (default) or Reconcile")),
		),
		Handler: CreateCreateClusterResourceSetHandler(serverCtx),
	})

	// capi_list_cluster_resource_sets
	tools = append(tools, ToolRegistration{
		Tool: mcp.NewTool(
			"capi_list_cluster_resource_sets",
			mcp.WithDescription("List ClusterResourceSets"),
			mcp.WithString("namespace", mcp.Description("Namespace to filter (optional, empty for all)")),
		),
		Handler: CreateListClusterResourceSetsHandler(serverCtx),
	})

	// capi_get_cluster_resource_set
	tools = append(tools, ToolRegistration{
		Tool: mcp.NewTool(
			"capi_get_cluster_resource_set",
			mcp.WithDescription("Get detailed information about a specific ClusterResourceSet"),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the ClusterResourceSet")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Name of the ClusterResourceSet")),
		),
		Handler: CreateGetClusterResourceSetHandler(serverCtx),
	})

	// capi_delete_cluster_resource_set
	tools = append(tools, ToolRegistration{
		Tool: mcp.NewTool(
			"capi_delete_cluster_resource_set",
			mcp.WithDescription("Delete a ClusterResourceSet. The Secrets/ConfigMaps it referenced are left in place."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the ClusterResourceSet")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Name of the ClusterResourceSet")),
		),
		Handler: CreateDeleteClusterResourceSetHandler(serverCtx),
	})

	return tools
}

// parseResourceSetResources parses the "resources" tool argument into
// capi.ClusterResourceSetResourceSpec values.
func parseResourceSetResources(raw interface{}) ([]capi.ClusterResourceSetResourceSpec, error) {
	rawList, ok := raw.([]interface{})
	if !ok || len(rawList) == 0 {
		return nil, fmt.Errorf("resources argument is required and must be a non-empty array")
	}

	resources := make([]capi.ClusterResourceSetResourceSpec, 0, len(rawList))
	for i, rawRes := range rawList {
		m, ok := rawRes.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("resources[%d] must be an object", i)
		}

		kind, _ := m["kind"].(string)
		name, _ := m["name"].(string)
		if kind == "" || name == "" {
			return nil, fmt.Errorf("resources[%d] requires kind and name", i)
		}

		var data map[string]string
		if rawData, ok := m["data"].(map[string]interface{}); ok {
			data = make(map[string]string, len(rawData))
			for k, v := range rawData {
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("resources[%d].data[%q] must be a string", i, k)
				}
				data[k] = s
			}
		}

		resources = append(resources, capi.ClusterResourceSetResourceSpec{
			Kind: kind,
			Name: name,
			Data: data,
		})
	}

	return resources, nil
}

// CreateCreateClusterResourceSetHandler creates a handler for creating a new ClusterResourceSet.
func CreateCreateClusterResourceSetHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()

		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}

		rawSelector, ok := arguments["cluster_selector"].(map[string]interface{})
		if !ok || len(rawSelector) == 0 {
			return mcp.NewToolResultError("cluster_selector argument is required"), nil
		}
		clusterSelector := make(map[string]string, len(rawSelector))
		for k, v := range rawSelector {
			s, ok := v.(string)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("cluster_selector[%q] must be a string", k)), nil
			}
			clusterSelector[k] = s
		}

		resources, err := parseResourceSetResources(arguments["resources"])
		if err != nil {
			return mcp.NewToolResultErrorFromErr("invalid resources", err), nil
		}

		strategy, _ := arguments["strategy"].(string)

		crs, err := serverCtx.CAPIClient.CreateClusterResourceSet(ctx, capi.CreateClusterResourceSetOptions{
			Name:            name,
			Namespace:       namespace,
			ClusterSelector: clusterSelector,
			Strategy:        strategy,
			Resources:       resources,
		})
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to create cluster resource set", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "ClusterResourceSet %s/%s created.\n", crs.Namespace, crs.Name)
		fmt.Fprintf(&content, "  Strategy: %s\n", crs.Spec.Strategy)
		fmt.Fprintf(&content, "  Cluster selector: %v\n", crs.Spec.ClusterSelector.MatchLabels)
		fmt.Fprintf(&content, "  Resources: %d\n", len(crs.Spec.Resources))
		content.WriteString("\nClusters matching the selector will have these resources applied automatically.\n")
		content.WriteString("Use capi_update_cluster to add the matching label to an existing Cluster.\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: content.String()},
			},
		}, nil
	}
}

// CreateListClusterResourceSetsHandler creates a handler for listing ClusterResourceSets.
func CreateListClusterResourceSetsHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, _ := arguments["namespace"].(string)

		list, err := serverCtx.CAPIClient.ListClusterResourceSets(ctx, namespace)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to list cluster resource sets", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Found %d ClusterResourceSets:\n\n", len(list.Items))
		for _, crs := range list.Items {
			fmt.Fprintf(&content, "ClusterResourceSet: %s/%s\n", crs.Namespace, crs.Name)
			fmt.Fprintf(&content, "  Strategy: %s\n", crs.Spec.Strategy)
			fmt.Fprintf(&content, "  Cluster selector: %v\n", crs.Spec.ClusterSelector.MatchLabels)
			fmt.Fprintf(&content, "  Resources: %d\n\n", len(crs.Spec.Resources))
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: content.String()},
			},
		}, nil
	}
}

// CreateGetClusterResourceSetHandler creates a handler for getting a ClusterResourceSet.
func CreateGetClusterResourceSetHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		crs, err := serverCtx.CAPIClient.GetClusterResourceSet(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster resource set", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "ClusterResourceSet: %s/%s\n", crs.Namespace, crs.Name)
		fmt.Fprintf(&content, "Strategy: %s\n", crs.Spec.Strategy)
		fmt.Fprintf(&content, "Cluster selector: %v\n", crs.Spec.ClusterSelector.MatchLabels)
		content.WriteString("Resources:\n")
		for _, res := range crs.Spec.Resources {
			fmt.Fprintf(&content, "  - %s/%s\n", res.Kind, res.Name)
		}
		fmt.Fprintf(&content, "Observed generation: %d\n", crs.Status.ObservedGeneration)
		for _, cond := range crs.Status.Conditions {
			fmt.Fprintf(&content, "Condition %s: %s (%s)\n", cond.Type, cond.Status, cond.Message)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: content.String()},
			},
		}, nil
	}
}

// CreateDeleteClusterResourceSetHandler creates a handler for deleting a ClusterResourceSet.
func CreateDeleteClusterResourceSetHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		if err := serverCtx.CAPIClient.DeleteClusterResourceSet(ctx, namespace, name); err != nil {
			return mcp.NewToolResultErrorFromErr("failed to delete cluster resource set", err), nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: fmt.Sprintf("ClusterResourceSet %s/%s deleted.\n", namespace, name)},
			},
		}, nil
	}
}
