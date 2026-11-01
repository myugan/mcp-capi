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
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		cluster, err := capiClient.CreateCluster(ctx, opts)
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

// CreateListClustersHandler creates a handler for listing CAPI clusters as
// {items: [ClusterSummary]}.
func CreateListClustersHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		clusters, err := capiClient.ListClusters(ctx, namespace, labelSelector)
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
		allMachines, err := capiClient.ListMachines(ctx, namespace, "")
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

		items := make([]ClusterSummary, 0, len(clusters.Items))
		for i := range clusters.Items {
			cluster := &clusters.Items[i]
			key := cluster.Namespace + "/" + cluster.Name
			status, _ := capiClient.GetClusterStatusFromList(ctx, cluster, machinesByCluster[key])
			if status != nil {
				items = append(items, clusterSummary(status))
			}
		}

		return listResult(items)
	}
}

// getClusterResult is the result of capi_get_cluster. When one cluster
// resolves, its summary is inlined; when the name only matched label values
// on several clusters, they are listed as candidates instead.
type getClusterResult struct {
	*ClusterSummary
	MatchedBy  string           `json:"matchedBy,omitempty"`
	Note       string           `json:"note,omitempty"`
	Candidates []ClusterSummary `json:"candidates,omitempty"`
}

// CreateGetClusterHandler creates a handler for getting a specific cluster
func CreateGetClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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
		status, err := capiClient.GetClusterStatus(ctx, namespace, name)
		if err == nil {
			summary := clusterSummary(status)
			return jsonResult(getClusterResult{ClusterSummary: &summary})
		}

		// If exact name match failed, try matching against label values
		matched, labelErr := capiClient.FindClustersByLabelValue(ctx, namespace, name)
		if labelErr != nil || len(matched.Items) == 0 {
			// Return the original error if label search also fails
			return mcp.NewToolResultError(fmt.Sprintf("failed to get cluster %q: no cluster found by name or label value in namespace %s", name, namespace)), nil
		}

		if len(matched.Items) == 1 {
			// Single match found via labels - return its status
			cluster := matched.Items[0]
			status, err := capiClient.GetClusterStatus(ctx, cluster.Namespace, cluster.Name)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
			}
			summary := clusterSummary(status)
			return jsonResult(getClusterResult{
				ClusterSummary: &summary,
				MatchedBy:      "labelValue",
				Note:           fmt.Sprintf("No cluster named %q found; matched cluster %s by label value.", name, cluster.Name),
			})
		}

		// Multiple matches - list them for the user to disambiguate
		candidates := make([]ClusterSummary, 0, len(matched.Items))
		for _, cluster := range matched.Items {
			status, err := capiClient.GetClusterStatus(ctx, cluster.Namespace, cluster.Name)
			if err == nil {
				candidates = append(candidates, clusterSummary(status))
			}
		}

		return jsonResult(getClusterResult{
			MatchedBy:  "labelValue",
			Note:       fmt.Sprintf("No cluster named %q found; %d clusters matched the term in their labels. Specify the exact cluster name.", name, len(matched.Items)),
			Candidates: candidates,
		})
	}
}

// CreateClusterStatusHandler creates a handler for getting detailed cluster status
func CreateClusterStatusHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		status, err := capiClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		return jsonResult(clusterSummary(status))
	}
}

// clusterHealthResult is the result of capi_cluster_health.
type clusterHealthResult struct {
	Namespace           string   `json:"namespace"`
	Name                string   `json:"name"`
	Healthy             bool     `json:"healthy"`
	ControlPlaneReady   bool     `json:"controlPlaneReady"`
	InfrastructureReady bool     `json:"infrastructureReady"`
	WorkersReady        bool     `json:"workersReady"`
	Issues              []string `json:"issues"`
	Warnings            []string `json:"warnings"`
	Recommendations     []string `json:"recommendations,omitempty"`
}

// CreateClusterHealthHandler creates a handler for checking cluster health
func CreateClusterHealthHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		health, err := capiClient.GetClusterHealth(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster health", err), nil
		}

		result := clusterHealthResult{
			Namespace:           namespace,
			Name:                name,
			Healthy:             health.Healthy,
			ControlPlaneReady:   health.ControlPlaneReady,
			InfrastructureReady: health.InfraReady,
			WorkersReady:        health.WorkersReady,
			Issues:              nonNilStrings(health.Issues),
			Warnings:            nonNilStrings(health.Warnings),
		}

		if !health.Healthy {
			if !health.ControlPlaneReady {
				result.Recommendations = append(result.Recommendations,
					"Check control plane pods and logs",
					"Verify API server connectivity")
			}
			if !health.InfraReady {
				result.Recommendations = append(result.Recommendations,
					"Check infrastructure provider status",
					"Verify cloud resources are provisioned")
			}
			if !health.WorkersReady {
				result.Recommendations = append(result.Recommendations,
					"Check machine status with capi_list_machines",
					"Review machine deployment events")
			}
		}

		return jsonResult(result)
	}
}

// nonNilStrings returns s, or an empty slice when s is nil, so the field
// encodes as [] rather than null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// scaleClusterResult is the result of capi_scale_cluster.
type scaleClusterResult struct {
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	Target            string `json:"target"`
	Replicas          int    `json:"replicas"`
	MachineDeployment string `json:"machineDeployment,omitempty"`
	Message           string `json:"message"`
}

// CreateScaleClusterHandler creates a handler for scaling clusters
func CreateScaleClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		err = capiClient.ScaleCluster(ctx, namespace, name, target, int(replicas), machineDeployment)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to scale cluster", err), nil
		}

		return jsonResult(scaleClusterResult{
			Namespace:         namespace,
			Name:              name,
			Target:            target,
			Replicas:          int(replicas),
			MachineDeployment: machineDeployment,
			Message:           fmt.Sprintf("Cluster %s/%s: %s scaled to %d replicas", namespace, name, target, int(replicas)),
		})
	}
}

// CreateKubectlHandler creates a handler that dynamically resolves a workload
// cluster's kubeconfig and runs an arbitrary kubectl invocation directly
// against that cluster's own API server.
func CreateKubectlHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		output, err := capiClient.ExecKubectl(ctx, namespace, clusterName, args, impersonateAs, stdin)
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
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		output, err := capiClient.ExecHelm(ctx, namespace, clusterName, args)
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

// kubeconfigResult is the result of capi_get_kubeconfig.
type kubeconfigResult struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Kubeconfig string `json:"kubeconfig"`
}

// CreateGetKubeconfigHandler creates a handler for retrieving cluster kubeconfig
func CreateGetKubeconfigHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		kubeconfig, err := capiClient.GetKubeconfig(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get kubeconfig", err), nil
		}

		return jsonResult(kubeconfigResult{
			Namespace:  namespace,
			Name:       name,
			Kubeconfig: kubeconfig,
		})
	}
}

// pauseResult is the result of capi_pause_cluster and capi_resume_cluster.
type pauseResult struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Paused    bool   `json:"paused"`
	Message   string `json:"message"`
}

// CreatePauseClusterHandler creates a handler for pausing cluster reconciliation
func CreatePauseClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		err = capiClient.PauseCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to pause cluster", err), nil
		}

		return jsonResult(pauseResult{
			Namespace: namespace,
			Name:      name,
			Paused:    true,
			Message: "Reconciliation paused: CAPI controllers make no changes to the cluster (no updates, no scaling) " +
				"until capi_resume_cluster is called, so manual operations are safe.",
		})
	}
}

// CreateResumeClusterHandler creates a handler for resuming cluster reconciliation
func CreateResumeClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
		arguments := request.GetArguments()
		namespace, ok := arguments["namespace"].(string)
		if !ok || namespace == "" {
			return mcp.NewToolResultError("namespace argument is required"), nil
		}
		name, ok := arguments["name"].(string)
		if !ok || name == "" {
			return mcp.NewToolResultError("name argument is required"), nil
		}

		err = capiClient.ResumeCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to resume cluster", err), nil
		}

		return jsonResult(pauseResult{
			Namespace: namespace,
			Name:      name,
			Paused:    false,
			Message: "Reconciliation resumed: CAPI controllers manage the cluster again and apply any pending " +
				"updates or scaling.",
		})
	}
}

// deleteClusterResult is the result of capi_delete_cluster.
type deleteClusterResult struct {
	Namespace       string         `json:"namespace"`
	Name            string         `json:"name"`
	Deleted         bool           `json:"deleted"`
	Cluster         ClusterSummary `json:"cluster"`
	Message         string         `json:"message"`
	Recommendations []string       `json:"recommendations,omitempty"`
}

// CreateDeleteClusterHandler creates a handler for deleting a cluster
func CreateDeleteClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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
		status, err := capiClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		result := deleteClusterResult{
			Namespace: namespace,
			Name:      name,
			Cluster:   clusterSummary(status),
		}

		// Safety check: a Ready cluster is only deleted with force=true.
		if !force && status.Ready {
			result.Deleted = false
			result.Message = "Safety check failed: the cluster is Ready and appears operational. Pass force=true to delete it anyway."
			result.Recommendations = []string{
				"Back up important data with velero; capi_backup_cluster exports none of the cluster's resources yet",
				"Migrate workloads to another cluster",
				"Confirm this is the intended cluster",
			}
			return jsonResult(result)
		}

		err = capiClient.DeleteCluster(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to delete cluster", err), nil
		}

		result.Deleted = true
		result.Message = "Deletion initiated. Cleaning up cluster resources, deprovisioning infrastructure and " +
			"processing finalizers can take several minutes; monitor with capi_list_clusters."
		return jsonResult(result)
	}
}

// upgradeClusterResult is the result of capi_upgrade_cluster.
type upgradeClusterResult struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	TargetVersion   string `json:"targetVersion"`
	UpgradeWorkers  bool   `json:"upgradeWorkers"`
	Message         string `json:"message"`
}

// CreateUpgradeClusterHandler creates a handler for upgrading cluster Kubernetes version
func CreateUpgradeClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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
		status, err := capiClient.GetClusterStatus(ctx, namespace, name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to get cluster status", err), nil
		}

		opts := capi.UpgradeClusterOptions{
			Namespace:      namespace,
			Name:           name,
			TargetVersion:  targetVersion,
			UpgradeWorkers: upgradeWorkers,
		}

		if err := capiClient.UpgradeCluster(ctx, opts); err != nil {
			return mcp.NewToolResultErrorFromErr("failed to upgrade cluster", err), nil
		}

		workers := "worker machines are upgraded after the control plane is ready"
		if !upgradeWorkers {
			workers = "worker machines are not upgraded (upgrade_workers=false)"
		}

		return jsonResult(upgradeClusterResult{
			Namespace:       namespace,
			Name:            name,
			PreviousVersion: status.Version,
			TargetVersion:   targetVersion,
			UpgradeWorkers:  upgradeWorkers,
			Message: fmt.Sprintf("Upgrade initiated. Control plane machines are upgraded first, one at a time; %s. "+
				"The rolling upgrade can take 30-60 minutes and workloads may be rescheduled; monitor with "+
				"capi_cluster_status, capi_cluster_health and capi_list_machines.", workers),
		})
	}
}

// updateClusterResult is the result of capi_update_cluster; labels and
// annotations are the cluster's user-managed metadata after the update.
type updateClusterResult struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Message     string            `json:"message"`
}

// CreateUpdateClusterHandler creates a handler for updating cluster metadata
func CreateUpdateClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		opts := capi.UpdateClusterOptions{
			Namespace:   namespace,
			Name:        name,
			Labels:      labelMap,
			Annotations: annotationMap,
		}

		cluster, err := capiClient.UpdateCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to update cluster", err), nil
		}

		return jsonResult(updateClusterResult{
			Namespace:   namespace,
			Name:        name,
			Labels:      capi.UserMetadata(cluster.Labels),
			Annotations: capi.UserMetadata(cluster.Annotations),
			Message:     "Cluster metadata updated",
		})
	}
}

// moveClusterResult is the result of capi_move_cluster.
type moveClusterResult struct {
	Namespace        string   `json:"namespace"`
	Name             string   `json:"name"`
	DryRun           bool     `json:"dryRun"`
	TargetKubeconfig string   `json:"targetKubeconfig,omitempty"`
	TargetNamespace  string   `json:"targetNamespace,omitempty"`
	Steps            []string `json:"steps"`
	Commands         []string `json:"commands"`
	Manifest         string   `json:"manifest"`
	Notes            []string `json:"notes"`
}

// CreateMoveClusterHandler creates a handler for moving clusters between management clusters
func CreateMoveClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		opts := capi.MoveClusterOptions{
			Namespace:        namespace,
			Name:             name,
			TargetKubeconfig: targetKubeconfig,
			TargetNamespace:  targetNamespace,
			DryRun:           dryRun,
		}

		// Get move instructions/manifest
		manifest, err := capiClient.MoveCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to prepare cluster move", err), nil
		}

		kubeconfigFlag := "<target-kubeconfig>"
		if targetKubeconfig != "" {
			kubeconfigFlag = targetKubeconfig
		}
		moveCommand := fmt.Sprintf("clusterctl move --to-kubeconfig=%s --namespace %s", kubeconfigFlag, namespace)
		if targetNamespace != "" && targetNamespace != namespace {
			moveCommand += " --to-namespace " + targetNamespace
		}

		notes := []string{
			"Nothing was moved: run the move command yourself",
			"clusterctl move pauses the source cluster and migrates its CAPI resources",
			"Ensure network connectivity between the management clusters",
			"Provider versions must match on both management clusters",
		}
		if dryRun {
			notes = append([]string{"dry_run changes nothing: this tool never moves anything, with or without it"}, notes...)
		}

		return jsonResult(moveClusterResult{
			Namespace:        namespace,
			Name:             name,
			DryRun:           dryRun,
			TargetKubeconfig: targetKubeconfig,
			TargetNamespace:  targetNamespace,
			Steps: []string{
				"Ensure the target management cluster is ready",
				"Install the required providers on the target management cluster",
				"Create the target namespace if needed",
				"Run clusterctl move (see commands); it pauses the source cluster itself",
			},
			Commands: []string{moveCommand},
			Manifest: manifest,
			Notes:    notes,
		})
	}
}

// backupClusterResult is the result of capi_backup_cluster.
type backupClusterResult struct {
	Namespace      string   `json:"namespace"`
	Name           string   `json:"name"`
	IncludeSecrets bool     `json:"includeSecrets"`
	Backup         string   `json:"backup"`
	Notes          []string `json:"notes"`
}

// CreateBackupClusterHandler creates a handler for backing up cluster configurations
func CreateBackupClusterHandler(serverCtx *ServerContext) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		capiClient, err := serverCtx.Client(ctx)
		if err != nil {
			return nil, err
		}
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

		opts := capi.BackupClusterOptions{
			Namespace:      namespace,
			Name:           name,
			IncludeSecrets: includeSecrets,
			OutputFormat:   outputFormat,
		}

		backup, err := capiClient.BackupCluster(ctx, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("failed to create cluster backup", err), nil
		}

		notes := []string{
			"A placeholder: no resources are exported, only these instructions (giantswarm/mcp-capi#303)",
			"Back up the cluster's resources and workload data with velero",
		}
		if includeSecrets {
			notes = append(notes, "include_secrets passed the credential-export check, but no Secret is exported either")
		}

		return jsonResult(backupClusterResult{
			Namespace:      namespace,
			Name:           name,
			IncludeSecrets: includeSecrets,
			Backup:         backup,
			Notes:          notes,
		})
	}
}
