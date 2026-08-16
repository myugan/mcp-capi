package capi

import (
	"context"
	"fmt"
	"math"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1"                      //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Provider represents an infrastructure provider
type Provider string

const (
	ProviderAWS     Provider = "aws"
	ProviderAzure   Provider = "azure"
	ProviderGCP     Provider = "gcp"
	ProviderVSphere Provider = "vsphere"
	ProviderVCD     Provider = "vcd"
	ProviderUnknown Provider = "unknown"
)

// InitializeProviders adds all provider schemes to the client
func (c *Client) InitializeProviders() error {
	scheme := c.ctrlClient.Scheme()

	// Add KubeadmControlPlane scheme
	if err := controlplanev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add KubeadmControlPlane to scheme: %w", err)
	}

	// Note: Infrastructure provider schemes would be added here
	// For now, we'll use unstructured resources for provider-specific resources

	return nil
}

// DetermineProvider returns the infrastructure provider for a cluster based on
// its InfrastructureRef.Kind. Unlike GetProviderForCluster, this is a pure
// function that requires no API call.
func DetermineProvider(cluster *clusterv1.Cluster) Provider {
	if cluster.Spec.InfrastructureRef == nil {
		return ProviderUnknown
	}

	switch cluster.Spec.InfrastructureRef.Kind {
	case "AWSCluster":
		return ProviderAWS
	case "AzureCluster":
		return ProviderAzure
	case "GCPCluster":
		return ProviderGCP
	case "VSphereCluster":
		return ProviderVSphere
	case "VCDCluster":
		return ProviderVCD
	default:
		return ProviderUnknown
	}
}

// GetProviderForCluster determines which infrastructure provider a cluster is using
func (c *Client) GetProviderForCluster(ctx context.Context, namespace, clusterName string) (Provider, error) {
	cluster, err := c.GetCluster(ctx, namespace, clusterName)
	if err != nil {
		return ProviderUnknown, err
	}

	return DetermineProvider(cluster), nil
}

// GetKubeadmControlPlane retrieves the KubeadmControlPlane for a cluster
func (c *Client) GetKubeadmControlPlane(ctx context.Context, namespace, name string) (*controlplanev1.KubeadmControlPlane, error) {
	kcp := &controlplanev1.KubeadmControlPlane{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, kcp); err != nil {
		return nil, fmt.Errorf("failed to get KubeadmControlPlane %s/%s: %w", namespace, name, err)
	}

	return kcp, nil
}

// ListKubeadmControlPlanes lists all KubeadmControlPlanes
func (c *Client) ListKubeadmControlPlanes(ctx context.Context, namespace string) (*controlplanev1.KubeadmControlPlaneList, error) {
	kcpList := &controlplanev1.KubeadmControlPlaneList{}

	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}

	if err := c.ctrlClient.List(ctx, kcpList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list KubeadmControlPlanes: %w", err)
	}

	return kcpList, nil
}

// GetInfrastructureResource retrieves an infrastructure-specific resource as unstructured
func (c *Client) GetInfrastructureResource(ctx context.Context, ref *client.ObjectKey, into client.Object) error {
	if err := c.ctrlClient.Get(ctx, *ref, into); err != nil {
		return fmt.Errorf("failed to get infrastructure resource: %w", err)
	}
	return nil
}

// ScaleControlPlane scales a KubeadmControlPlane to the specified number of replicas
func (c *Client) ScaleControlPlane(ctx context.Context, namespace, name string, replicas int32) error {
	kcp, err := c.GetKubeadmControlPlane(ctx, namespace, name)
	if err != nil {
		return err
	}

	// Update replicas
	kcp.Spec.Replicas = &replicas

	if err := c.ctrlClient.Update(ctx, kcp); err != nil {
		return fmt.Errorf("failed to scale control plane: %w", err)
	}

	return nil
}

// ScaleCluster scales either control plane or worker nodes of a cluster.
//
// For ClusterClass-managed clusters (Cluster.Spec.Topology set), the
// topology controller treats spec.topology as the sole source of truth and
// reconciles any direct KubeadmControlPlane/MachineDeployment replica patch
// straight back to whatever the topology spec says. So for those clusters
// the replica count is patched on the Cluster's topology spec instead --
// the change that actually sticks.
func (c *Client) ScaleCluster(ctx context.Context, namespace, clusterName, target string, replicas int, machineDeploymentName string) error {
	if replicas < 0 || replicas > math.MaxInt32 {
		return fmt.Errorf("replicas %d out of range for int32", replicas)
	}
	r := int32(replicas) //#nosec G115 -- bounded above

	cluster, err := c.GetCluster(ctx, namespace, clusterName)
	if err != nil {
		return err
	}

	if cluster.Spec.Topology != nil {
		return c.scaleClusterTopology(ctx, cluster, target, r, machineDeploymentName)
	}

	switch target {
	case "controlplane":
		return c.ScaleControlPlane(ctx, namespace, clusterName, r)
	case "workers":
		if machineDeploymentName == "" {
			return fmt.Errorf("machineDeployment name is required when scaling workers")
		}
		return c.ScaleMachineDeployment(ctx, namespace, machineDeploymentName, r)
	default:
		return fmt.Errorf("invalid target: %s (must be 'controlplane' or 'workers')", target)
	}
}

// scaleClusterTopology patches spec.topology on a ClusterClass-managed
// Cluster so the topology controller's reconcile converges on the new
// replica count instead of reverting it.
func (c *Client) scaleClusterTopology(ctx context.Context, cluster *clusterv1.Cluster, target string, replicas int32, machineDeploymentName string) error {
	switch target {
	case "controlplane":
		cluster.Spec.Topology.ControlPlane.Replicas = &replicas
	case "workers":
		if machineDeploymentName == "" {
			return fmt.Errorf("machineDeployment name is required when scaling workers")
		}
		if cluster.Spec.Topology.Workers == nil {
			return fmt.Errorf("cluster %s/%s has no worker machine deployments in its topology", cluster.Namespace, cluster.Name)
		}

		topologyName, err := c.resolveMachineDeploymentTopologyName(ctx, cluster.Namespace, machineDeploymentName)
		if err != nil {
			return err
		}

		found := false
		for i := range cluster.Spec.Topology.Workers.MachineDeployments {
			md := &cluster.Spec.Topology.Workers.MachineDeployments[i]
			if md.Name == topologyName {
				md.Replicas = &replicas
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("machine deployment %q not found in cluster %s/%s topology", machineDeploymentName, cluster.Namespace, cluster.Name)
		}
	default:
		return fmt.Errorf("invalid target: %s (must be 'controlplane' or 'workers')", target)
	}

	if err := c.ctrlClient.Update(ctx, cluster); err != nil {
		return fmt.Errorf("failed to scale cluster topology: %w", err)
	}

	return nil
}

// resolveMachineDeploymentTopologyName resolves a machineDeployment argument
// to the name used in Cluster.Spec.Topology.Workers.MachineDeployments[].Name
// (e.g. "md-0"). Callers may pass either that topology name directly, or the
// generated MachineDeployment resource name (e.g. "mycluster-md-0-wqd6g",
// as returned by ListMachineDeployments) -- the latter is resolved via the
// topology name label CAPI sets on the generated resource.
func (c *Client) resolveMachineDeploymentTopologyName(ctx context.Context, namespace, machineDeploymentName string) (string, error) {
	md, err := c.GetMachineDeployment(ctx, namespace, machineDeploymentName)
	if err != nil {
		// Not a generated resource name -- assume it's already a topology name.
		return machineDeploymentName, nil
	}
	if topologyName, ok := md.Labels[clusterv1.ClusterTopologyMachineDeploymentNameLabel]; ok && topologyName != "" {
		return topologyName, nil
	}
	return machineDeploymentName, nil
}

// ScaleMachineDeployment scales a MachineDeployment to the specified number of replicas
func (c *Client) ScaleMachineDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	md, err := c.GetMachineDeployment(ctx, namespace, name)
	if err != nil {
		return err
	}

	// Update replicas
	md.Spec.Replicas = &replicas

	if err := c.ctrlClient.Update(ctx, md); err != nil {
		return fmt.Errorf("failed to scale machine deployment: %w", err)
	}

	return nil
}
