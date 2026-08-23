package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration

	"github.com/giantswarm/mcp-capi/pkg/capi"
)

// CreateCreateClusterHandler creates a handler for creating a new cluster
// from an existing ClusterClass (topology-based).
func CreateCreateClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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
		clusterClass, ok := arguments["cluster_class"].(string)
		if !ok || clusterClass == "" {
			return mcp.NewToolResultError("cluster_class argument is required"), nil
		}
		kubernetesVersion, ok := arguments["kubernetes_version"].(string)
		if !ok || kubernetesVersion == "" {
			return mcp.NewToolResultError("kubernetes_version argument is required"), nil
		}

		controlPlaneReplicas := int32(1)
		if v, ok := arguments["control_plane_replicas"].(float64); ok {
			controlPlaneReplicas = int32(v)
		}

		var machineDeployments []capi.MachineDeploymentSpec
		if rawMDs, ok := arguments["machine_deployments"].([]interface{}); ok {
			for _, raw := range rawMDs {
				m, ok := raw.(map[string]interface{})
				if !ok {
					return mcp.NewToolResultError("each machine_deployments entry must be an object"), nil
				}
				class, _ := m["class"].(string)
				mdName, _ := m["name"].(string)
				if class == "" || mdName == "" {
					return mcp.NewToolResultError("each machine_deployments entry requires class and name"), nil
				}
				replicas := int32(0)
				if r, ok := m["replicas"].(float64); ok {
					replicas = int32(r)
				}
				var mdVariables map[string]interface{}
				if rawMDVars, ok := m["variables"].(map[string]interface{}); ok {
					mdVariables = rawMDVars
				}
				machineDeployments = append(machineDeployments, capi.MachineDeploymentSpec{
					Class:     class,
					Name:      mdName,
					Replicas:  replicas,
					Variables: mdVariables,
				})
			}
		}

		variables := map[string]interface{}{}
		if rawVars, ok := arguments["variables"].(map[string]interface{}); ok {
			variables = rawVars
		}

		var clusterNetwork *capi.ClusterNetworkSpec
		if rawNet, ok := arguments["cluster_network"].(map[string]interface{}); ok {
			clusterNetwork = &capi.ClusterNetworkSpec{}
			if rawPods, ok := rawNet["pods"].([]interface{}); ok {
				for _, p := range rawPods {
					s, ok := p.(string)
					if !ok {
						return mcp.NewToolResultError("cluster_network.pods entries must be strings"), nil
					}
					clusterNetwork.Pods = append(clusterNetwork.Pods, s)
				}
			}
			if rawServices, ok := rawNet["services"].([]interface{}); ok {
				for _, sv := range rawServices {
					s, ok := sv.(string)
					if !ok {
						return mcp.NewToolResultError("cluster_network.services entries must be strings"), nil
					}
					clusterNetwork.Services = append(clusterNetwork.Services, s)
				}
			}
			clusterNetwork.ServiceDomain, _ = rawNet["service_domain"].(string)
		}

		opts := capi.CreateClusterOptions{
			Name:                 name,
			Namespace:            namespace,
			ClusterClass:         clusterClass,
			KubernetesVersion:    kubernetesVersion,
			ControlPlaneReplicas: controlPlaneReplicas,
			MachineDeployments:   machineDeployments,
			Variables:            variables,
			ClusterNetwork:       clusterNetwork,
		}

		cluster, err := serverCtx.CAPIClient.CreateCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to create cluster", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Cluster %s/%s created from ClusterClass %s.\n", cluster.Namespace, cluster.Name, clusterClass)
		fmt.Fprintf(&content, "  Kubernetes version: %s\n", kubernetesVersion)
		fmt.Fprintf(&content, "  Control plane replicas: %d\n", controlPlaneReplicas)
		fmt.Fprintf(&content, "  Machine deployments: %d\n", len(machineDeployments))
		for _, md := range machineDeployments {
			if len(md.Variables) > 0 {
				fmt.Fprintf(&content, "    %s: %d variable override(s)\n", md.Name, len(md.Variables))
			}
		}
		if clusterNetwork != nil {
			fmt.Fprintf(&content, "  Cluster network: pods=%v services=%v serviceDomain=%q\n",
				clusterNetwork.Pods, clusterNetwork.Services, clusterNetwork.ServiceDomain)
		}
		content.WriteString("\nUse capi_cluster_status to monitor provisioning.\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createListClustersHandler creates a handler for listing CAPI clusters
func CreateListClustersHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, _ := arguments["namespace"].(string)
		search, _ := arguments["search"].(string)

		// Parse label_selector from arguments
		var labelSelector map[string]string
		if ls, ok := arguments["label_selector"].(map[string]interface{}); ok && len(ls) > 0 {
			labelSelector = make(map[string]string)
			for k, v := range ls {
				if strVal, ok := v.(string); ok {
					labelSelector[k] = strVal
				}
			}
		}

		clusters, err := serverCtx.CAPIClient.ListClusters(ctx, namespace, labelSelector)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to list clusters", err), nil
		}

		// If a search term is provided, filter clusters by name or label values
		if search != "" {
			searchLower := strings.ToLower(search)
			var filtered []clusterv1.Cluster
			for _, cluster := range clusters.Items {
				if strings.Contains(strings.ToLower(cluster.Name), searchLower) {
					filtered = append(filtered, cluster)
					continue
				}
				matched := false
				for _, v := range cluster.Labels {
					if strings.Contains(strings.ToLower(v), searchLower) {
						matched = true
						break
					}
				}
				if matched {
					filtered = append(filtered, cluster)
				}
			}
			clusters.Items = filtered
		}

		// Bulk fetch all machines in the namespace to avoid N+1 queries
		allMachines, err := serverCtx.CAPIClient.ListMachines(ctx, namespace, "")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to list machines", err), nil
		}

		// Group machines by cluster name
		machinesByCluster := make(map[string][]clusterv1.Machine)
		for _, m := range allMachines.Items {
			clusterName := m.Labels[clusterv1.ClusterNameLabel]
			key := m.Namespace + "/" + clusterName
			machinesByCluster[key] = append(machinesByCluster[key], m)
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Found %d clusters:\n\n", len(clusters.Items))

		for i := range clusters.Items {
			cluster := &clusters.Items[i]
			key := cluster.Namespace + "/" + cluster.Name
			status, _ := serverCtx.CAPIClient.GetClusterStatusFromList(ctx, cluster, machinesByCluster[key])
			if status != nil {
				content.WriteString(capi.FormatClusterInfo(status))
				content.WriteString("\n---\n\n")
			}
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createGetClusterHandler creates a handler for getting a specific cluster
func CreateGetClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		// Try exact name match first
		status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, namespace, name)
		if err == nil {
			var content strings.Builder
			content.WriteString(capi.FormatClusterInfo(status))
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{
						Type: textContentType,
						Text: content.String(),
					},
				},
			}, nil
		}

		// If exact name match failed, try matching against label values
		matched, labelErr := serverCtx.CAPIClient.FindClustersByLabelValue(ctx, namespace, name)
		if labelErr != nil || len(matched.Items) == 0 {
			// Return the original error if label search also fails
			return mcp.NewToolResultError(fmt.Sprintf("failed to get cluster %q: no cluster found by name or label value in namespace %s", name, namespace)), nil
		}

		if len(matched.Items) == 1 {
			// Single match found via labels - return its status
			cluster := matched.Items[0]
			status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, cluster.Namespace, cluster.Name)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
			}
			var content strings.Builder
			fmt.Fprintf(&content, "Note: No cluster named %q found. Matched cluster by label value:\n\n", name)
			content.WriteString(capi.FormatClusterInfo(status))
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{
						Type: textContentType,
						Text: content.String(),
					},
				},
			}, nil
		}

		// Multiple matches - list them for the user to disambiguate
		var content strings.Builder
		fmt.Fprintf(&content, "No cluster named %q found, but %d clusters matched the term in their labels:\n\n", name, len(matched.Items))
		for _, cluster := range matched.Items {
			status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, cluster.Namespace, cluster.Name)
			if err == nil {
				content.WriteString(capi.FormatClusterInfo(status))
				content.WriteString("\n---\n\n")
			}
		}
		content.WriteString("Please specify the exact cluster name from the list above.")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createClusterStatusHandler creates a handler for getting detailed cluster status
func CreateClusterStatusHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		var content strings.Builder
		content.WriteString(capi.FormatClusterInfo(status))

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createClusterHealthHandler creates a handler for checking cluster health
func CreateClusterHealthHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		health, err := serverCtx.CAPIClient.GetClusterHealth(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster health", err), nil
		}

		var content strings.Builder

		// Overall status
		if health.Healthy {
			fmt.Fprintf(&content, "✅ Cluster %s/%s is HEALTHY\n\n", namespace, name)
		} else {
			fmt.Fprintf(&content, "❌ Cluster %s/%s is UNHEALTHY\n\n", namespace, name)
		}

		// Component status
		content.WriteString("Component Status:\n")
		fmt.Fprintf(&content, "  • Control Plane: %s\n", formatHealthStatus(health.ControlPlaneReady))
		fmt.Fprintf(&content, "  • Infrastructure: %s\n", formatHealthStatus(health.InfraReady))
		fmt.Fprintf(&content, "  • Worker Nodes: %s\n", formatHealthStatus(health.WorkersReady))

		// Issues
		if len(health.Issues) > 0 {
			content.WriteString("\n🔴 Issues:\n")
			for _, issue := range health.Issues {
				fmt.Fprintf(&content, "  • %s\n", issue)
			}
		}

		// Warnings
		if len(health.Warnings) > 0 {
			content.WriteString("\n⚠️  Warnings:\n")
			for _, warning := range health.Warnings {
				fmt.Fprintf(&content, "  • %s\n", warning)
			}
		}

		// Recommendations
		if !health.Healthy {
			content.WriteString("\n📋 Recommendations:\n")
			if !health.ControlPlaneReady {
				content.WriteString("  • Check control plane pods and logs\n")
				content.WriteString("  • Verify API server connectivity\n")
			}
			if !health.InfraReady {
				content.WriteString("  • Check infrastructure provider status\n")
				content.WriteString("  • Verify cloud resources are provisioned\n")
			}
			if !health.WorkersReady {
				content.WriteString("  • Check machine status with 'capi_list_machines'\n")
				content.WriteString("  • Review machine deployment events\n")
			}
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// formatHealthStatus returns a formatted string for component health status
func formatHealthStatus(ready bool) string {
	if ready {
		return "✅ Ready"
	}
	return "❌ Not Ready"
}

// createScaleClusterHandler creates a handler for scaling clusters
func CreateScaleClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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
		target, ok := arguments["target"].(string)
		if !ok || target == "" {
			return mcp.NewToolResultError("target argument is required"), nil
		}
		replicas, ok := arguments["replicas"].(float64)
		if !ok {
			return mcp.NewToolResultError("replicas argument is required and must be a number"), nil
		}
		machineDeployment, _ := arguments["machineDeployment"].(string)

		err := serverCtx.CAPIClient.ScaleCluster(ctx, namespace, name, target, int(replicas), machineDeployment)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to scale cluster", err), nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: fmt.Sprintf("Cluster %s/%s scaled successfully", namespace, name),
				},
			},
		}, nil
	}
}

// CreateKubectlHandler creates a handler that dynamically resolves a workload
// cluster's kubeconfig and runs an arbitrary kubectl invocation directly
// against that cluster's own API server.
func CreateKubectlHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		clusterName, ok := arguments["cluster_name"].(string)
		if !ok || clusterName == "" {
			return mcp.NewToolResultError("cluster_name argument is required"), nil
		}

		rawArgs, ok := arguments["args"].([]interface{})
		if !ok || len(rawArgs) == 0 {
			return mcp.NewToolResultError("args argument is required and must be a non-empty array of strings"), nil
		}
		args := make([]string, 0, len(rawArgs))
		for _, a := range rawArgs {
			s, ok := a.(string)
			if !ok {
				return mcp.NewToolResultError("all elements of args must be strings"), nil
			}
			args = append(args, s)
		}

		impersonateAs, _ := arguments["as"].(string)
		stdin, _ := arguments["stdin"].(string)

		output, err := serverCtx.CAPIClient.ExecKubectl(ctx, namespace, clusterName, args, impersonateAs, stdin)
		if err != nil {
			return mcp.NewToolResultErrorFromErr(fmt.Sprintf("kubectl execution failed\noutput:\n%s", output), err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "kubectl %s against cluster %s:\n\n", strings.Join(args, " "), clusterName)
		content.WriteString(output)

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: content.String(),
				},
			},
		}, nil
	}
}

// CreateHelmHandler creates a handler that dynamically resolves a workload
// cluster's kubeconfig and runs an arbitrary helm invocation directly
// against that cluster's own API server.
func CreateHelmHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		clusterName, ok := arguments["cluster_name"].(string)
		if !ok || clusterName == "" {
			return mcp.NewToolResultError("cluster_name argument is required"), nil
		}

		rawArgs, ok := arguments["args"].([]interface{})
		if !ok || len(rawArgs) == 0 {
			return mcp.NewToolResultError("args argument is required and must be a non-empty array of strings"), nil
		}
		args := make([]string, 0, len(rawArgs))
		for _, a := range rawArgs {
			s, ok := a.(string)
			if !ok {
				return mcp.NewToolResultError("all elements of args must be strings"), nil
			}
			args = append(args, s)
		}

		output, err := serverCtx.CAPIClient.ExecHelm(ctx, namespace, clusterName, args)
		if err != nil {
			return mcp.NewToolResultErrorFromErr(fmt.Sprintf("helm execution failed\noutput:\n%s", output), err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "helm %s against cluster %s:\n\n", strings.Join(args, " "), clusterName)
		content.WriteString(output)

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createGetKubeconfigHandler creates a handler for retrieving cluster kubeconfig
func CreateGetKubeconfigHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		kubeconfig, err := serverCtx.CAPIClient.GetKubeconfig(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get kubeconfig", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "Kubeconfig for cluster %s/%s:\n\n", namespace, name)
		content.WriteString("```yaml\n")
		content.WriteString(kubeconfig)
		content.WriteString("\n```\n\n")
		content.WriteString("To use this kubeconfig:\n")
		content.WriteString("1. Save the content between the ``` markers to a file (e.g., cluster-kubeconfig.yaml)\n")
		content.WriteString("2. Use it with kubectl: kubectl --kubeconfig=cluster-kubeconfig.yaml get nodes\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createPauseClusterHandler creates a handler for pausing cluster reconciliation
func CreatePauseClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		err := serverCtx.CAPIClient.PauseCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to pause cluster", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "✅ Cluster %s/%s has been paused\n\n", namespace, name)
		content.WriteString("The cluster reconciliation has been stopped. This means:\n")
		content.WriteString("- CAPI controllers will not make any changes to the cluster\n")
		content.WriteString("- The cluster will not be updated or scaled automatically\n")
		content.WriteString("- Manual operations can be performed safely\n\n")
		content.WriteString("To resume normal operations, use the capi_resume_cluster tool.")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createResumeClusterHandler creates a handler for resuming cluster reconciliation
func CreateResumeClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		err := serverCtx.CAPIClient.ResumeCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to resume cluster", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "✅ Cluster %s/%s has been resumed\n\n", namespace, name)
		content.WriteString("The cluster reconciliation has been restarted. This means:\n")
		content.WriteString("- CAPI controllers will now reconcile the cluster normally\n")
		content.WriteString("- Any pending updates or changes will be applied\n")
		content.WriteString("- Automatic scaling and updates are re-enabled\n\n")
		content.WriteString("The cluster is now under normal CAPI management.")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createDeleteClusterHandler creates a handler for deleting a cluster
func CreateDeleteClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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
		force, _ := arguments["force"].(bool)

		// Get cluster status first to show what will be deleted
		status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		var content strings.Builder

		// Show cluster information
		content.WriteString("⚠️  WARNING: You are about to delete the following cluster:\n\n")
		content.WriteString(capi.FormatClusterInfo(status))
		content.WriteString("\n")

		// Safety checks if not forced
		if !force {
			if status.Ready {
				content.WriteString("❌ SAFETY CHECK FAILED: Cluster is currently in Ready state.\n")
				content.WriteString("   This cluster appears to be healthy and operational.\n")
				content.WriteString("   Use force=true to override this safety check.\n\n")
				content.WriteString("   Recommended actions before deletion:\n")
				content.WriteString("   1. Backup any important data\n")
				content.WriteString("   2. Migrate workloads to another cluster\n")
				content.WriteString("   3. Ensure this is the correct cluster\n")

				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.TextContent{
							Type: textContentType,
							Text: content.String(),
						},
					},
				}, nil
			}
		}

		// Proceed with deletion
		err = serverCtx.CAPIClient.DeleteCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to delete cluster", err), nil
		}

		fmt.Fprintf(&content, "\n✅ Cluster %s/%s deletion initiated successfully.\n\n", namespace, name)
		content.WriteString("Note: The actual deletion process may take several minutes as:\n")
		content.WriteString("- All cluster resources are being cleaned up\n")
		content.WriteString("- Infrastructure resources are being deprovisioned\n")
		content.WriteString("- Finalizers are being processed\n\n")
		content.WriteString("You can monitor the deletion progress by listing clusters in this namespace.")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createUpgradeClusterHandler creates a handler for upgrading cluster Kubernetes version
func CreateUpgradeClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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
		targetVersion, ok := arguments["target_version"].(string)
		if !ok || targetVersion == "" {
			return mcp.NewToolResultError("target_version argument is required"), nil
		}

		// Default to upgrading workers
		upgradeWorkers := true
		if uw, ok := arguments["upgrade_workers"].(bool); ok {
			upgradeWorkers = uw
		}

		// Get current cluster status
		status, err := serverCtx.CAPIClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "🚀 Initiating cluster upgrade for %s/%s\n\n", namespace, name)
		content.WriteString("Current State:\n")
		fmt.Fprintf(&content, "  • Current Version: %s\n", status.Version)
		fmt.Fprintf(&content, "  • Target Version: %s\n", targetVersion)
		fmt.Fprintf(&content, "  • Upgrade Workers: %v\n\n", upgradeWorkers)

		// Perform the upgrade
		opts := capi.UpgradeClusterOptions{
			Namespace:      namespace,
			Name:           name,
			TargetVersion:  targetVersion,
			UpgradeWorkers: upgradeWorkers,
		}

		if err := serverCtx.CAPIClient.UpgradeCluster(ctx, opts); err != nil {
			return mcp.NewToolResultErrorFromErr("failed to upgrade cluster", err), nil
		}

		content.WriteString("✅ Upgrade initiated successfully!\n\n")
		content.WriteString("Upgrade Process:\n")
		content.WriteString("1. Control plane nodes will be upgraded first (one by one)\n")
		if upgradeWorkers {
			content.WriteString("2. Worker nodes will be upgraded after control plane is ready\n")
		} else {
			content.WriteString("2. Worker nodes will NOT be upgraded (upgrade_workers=false)\n")
		}
		content.WriteString("\n⚠️  Important Notes:\n")
		content.WriteString("• The upgrade process can take 30-60 minutes depending on cluster size\n")
		content.WriteString("• Control plane will remain available during rolling upgrade\n")
		content.WriteString("• Workloads may be rescheduled during worker node upgrades\n")
		content.WriteString("• Monitor progress with: capi_cluster_status\n")
		content.WriteString("\n📋 Recommended Actions:\n")
		content.WriteString("1. Monitor cluster health: capi_cluster_health\n")
		content.WriteString("2. Watch control plane: capi_list_machines\n")
		content.WriteString("3. Check events for any issues\n")
		content.WriteString("4. Verify workloads after upgrade completes\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createUpdateClusterHandler creates a handler for updating cluster metadata
func CreateUpdateClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		// Get labels and annotations from arguments
		labels, _ := arguments["labels"].(map[string]interface{})
		annotations, _ := arguments["annotations"].(map[string]interface{})

		// Convert interface{} maps to string maps
		labelMap := make(map[string]string)
		for k, v := range labels {
			if strVal, ok := v.(string); ok {
				labelMap[k] = strVal
			}
		}

		annotationMap := make(map[string]string)
		for k, v := range annotations {
			if strVal, ok := v.(string); ok {
				annotationMap[k] = strVal
			}
		}

		// Update the cluster
		opts := capi.UpdateClusterOptions{
			Namespace:   namespace,
			Name:        name,
			Labels:      labelMap,
			Annotations: annotationMap,
		}

		cluster, err := serverCtx.CAPIClient.UpdateCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to update cluster", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "✅ Cluster %s/%s updated successfully!\n\n", namespace, name)

		// Show what was updated
		if len(labelMap) > 0 {
			content.WriteString("Labels updated:\n")
			for k, v := range labelMap {
				if v == "" {
					fmt.Fprintf(&content, "  ✗ Removed: %s\n", k)
				} else {
					fmt.Fprintf(&content, "  ✓ Set: %s=%s\n", k, v)
				}
			}
			content.WriteString("\n")
		}

		if len(annotationMap) > 0 {
			content.WriteString("Annotations updated:\n")
			for k, v := range annotationMap {
				if v == "" {
					fmt.Fprintf(&content, "  ✗ Removed: %s\n", k)
				} else {
					fmt.Fprintf(&content, "  ✓ Set: %s=%s\n", k, v)
				}
			}
			content.WriteString("\n")
		}

		// Show current metadata
		content.WriteString("Current metadata:\n")
		content.WriteString("Labels:\n")
		if len(cluster.Labels) > 0 {
			for k, v := range cluster.Labels {
				fmt.Fprintf(&content, "  %s: %s\n", k, v)
			}
		} else {
			content.WriteString("  (none)\n")
		}

		content.WriteString("\nAnnotations:\n")
		if len(cluster.Annotations) > 0 {
			for k, v := range cluster.Annotations {
				fmt.Fprintf(&content, "  %s: %s\n", k, v)
			}
		} else {
			content.WriteString("  (none)\n")
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createMoveClusterHandler creates a handler for moving clusters between management clusters
func CreateMoveClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		targetKubeconfig, _ := arguments["target_kubeconfig"].(string)
		targetNamespace, _ := arguments["target_namespace"].(string)
		dryRun, _ := arguments["dry_run"].(bool)

		// Prepare move options
		opts := capi.MoveClusterOptions{
			Namespace:        namespace,
			Name:             name,
			TargetKubeconfig: targetKubeconfig,
			TargetNamespace:  targetNamespace,
			DryRun:           dryRun,
		}

		// Get move instructions/manifest
		manifest, err := serverCtx.CAPIClient.MoveCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to prepare cluster move", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "🚀 Cluster Move Preparation for %s/%s\n\n", namespace, name)

		if dryRun {
			content.WriteString("⚠️  DRY RUN MODE - No actual changes will be made\n\n")
		}

		content.WriteString("📋 Move Instructions:\n")
		content.WriteString("1. Ensure target management cluster is ready\n")
		content.WriteString("2. Install required providers on target cluster\n")
		content.WriteString("3. Create target namespace if needed\n")
		content.WriteString("4. Use clusterctl to perform the move:\n\n")

		content.WriteString("```bash\n")
		content.WriteString("# Pause the cluster first\n")
		fmt.Fprintf(&content, "kubectl patch cluster %s -n %s --type merge -p '{\"spec\":{\"paused\":true}}'\n\n", name, namespace)

		content.WriteString("# Move the cluster\n")
		if targetKubeconfig != "" {
			fmt.Fprintf(&content, "clusterctl move --to-kubeconfig=%s", targetKubeconfig)
		} else {
			content.WriteString("clusterctl move --to-kubeconfig=<target-kubeconfig>")
		}
		if targetNamespace != "" && targetNamespace != namespace {
			fmt.Fprintf(&content, " --namespace %s --to-namespace %s", namespace, targetNamespace)
		} else {
			fmt.Fprintf(&content, " --namespace %s", namespace)
		}
		content.WriteString("\n")
		content.WriteString("```\n\n")

		content.WriteString("⚠️  Important Notes:\n")
		content.WriteString("• The source cluster will be paused during move\n")
		content.WriteString("• All cluster resources will be migrated\n")
		content.WriteString("• Ensure network connectivity between clusters\n")
		content.WriteString("• Verify provider versions match\n\n")

		content.WriteString("📝 Move Manifest Preview:\n")
		content.WriteString("```yaml\n")
		content.WriteString(manifest)
		content.WriteString("\n```\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}

// createBackupClusterHandler creates a handler for backing up cluster configurations
func CreateBackupClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
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

		includeSecrets, _ := arguments["include_secrets"].(bool)
		outputFormat, _ := arguments["output_format"].(string)
		if outputFormat == "" {
			outputFormat = "yaml"
		}

		// Create backup
		opts := capi.BackupClusterOptions{
			Namespace:      namespace,
			Name:           name,
			IncludeSecrets: includeSecrets,
			OutputFormat:   outputFormat,
		}

		backup, err := serverCtx.CAPIClient.BackupCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to create cluster backup", err), nil
		}

		var content strings.Builder
		fmt.Fprintf(&content, "📦 Cluster Backup for %s/%s\n\n", namespace, name)

		content.WriteString("Backup Configuration:\n")
		fmt.Fprintf(&content, "  • Format: %s\n", outputFormat)
		fmt.Fprintf(&content, "  • Include Secrets: %v\n\n", includeSecrets)

		content.WriteString("📋 Backup Instructions:\n")
		content.WriteString("1. Save the backup content below to a file\n")
		content.WriteString("2. Store in a secure location (git, S3, etc.)\n")
		content.WriteString("3. Test restore procedure in a non-production environment\n\n")

		content.WriteString("🔧 Recommended Backup Tools:\n")
		content.WriteString("• Velero - Complete cluster backup solution\n")
		content.WriteString("  velero backup create <backup-name> --include-namespaces=<namespace>\n")
		content.WriteString("• etcd snapshot - For control plane state\n")
		content.WriteString("• Git repositories - For GitOps managed clusters\n\n")

		content.WriteString("⚠️  Important Notes:\n")
		content.WriteString("• This backup includes CAPI resources only\n")
		content.WriteString("• Workload data is NOT included\n")
		content.WriteString("• Infrastructure provider resources may need separate backup\n")
		if includeSecrets {
			content.WriteString("• ⚠️  Secrets are included - handle with care!\n")
		}
		content.WriteString("\n")

		content.WriteString("📄 Backup Content:\n")
		content.WriteString("```" + outputFormat + "\n")
		content.WriteString(backup)
		content.WriteString("\n```\n\n")

		content.WriteString("💾 To save this backup:\n")
		content.WriteString("1. Copy the content between the ``` markers\n")
		fmt.Fprintf(&content, "2. Save to a file: cluster-%s-%s-backup.%s\n", namespace, name, outputFormat)
		content.WriteString("3. Encrypt if it contains secrets\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: textContentType,
					Text: content.String(),
				},
			},
		}, nil
	}
}
