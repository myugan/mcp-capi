package capi

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newFakeClient builds a Client backed by a controller-runtime fake client,
// seeded with the given objects. Sufficient for exercising CreateCluster's
// object-construction logic without a real API server: the fake client still
// round-trips objects through the scheme, catching type/serialization bugs
// that pure struct construction would miss.
func newFakeClient(t *testing.T, objs ...client.Object) *Client {
	t.Helper()

	scheme := k8sruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add client-go scheme: %v", err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add CAPI scheme: %v", err)
	}

	ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	c := &Client{}
	c.SetClients(nil, ctrlClient)
	return c
}

func newTestClusterClass(namespace, name string) *clusterv1.ClusterClass {
	return &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func TestCreateClusterSetsClusterNetwork(t *testing.T) {
	c := newFakeClient(t, newTestClusterClass("default", "capn-audit"))

	cluster, err := c.CreateCluster(context.Background(), CreateClusterOptions{
		Name:                 "timbernetes",
		Namespace:            "default",
		ClusterClass:         "capn-audit",
		KubernetesVersion:    "v1.35.0",
		ControlPlaneReplicas: 1,
		ClusterNetwork: &ClusterNetworkSpec{
			Pods:          []string{"10.244.0.0/16"},
			Services:      []string{"10.96.0.0/12"},
			ServiceDomain: "cluster.local",
		},
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	if cluster.Spec.ClusterNetwork == nil {
		t.Fatal("expected Spec.ClusterNetwork to be set, got nil")
	}
	if got := cluster.Spec.ClusterNetwork.Pods.CIDRBlocks; len(got) != 1 || got[0] != "10.244.0.0/16" {
		t.Errorf("Pods CIDR = %v, want [10.244.0.0/16]", got)
	}
	if got := cluster.Spec.ClusterNetwork.Services.CIDRBlocks; len(got) != 1 || got[0] != "10.96.0.0/12" {
		t.Errorf("Services CIDR = %v, want [10.96.0.0/12]", got)
	}
	if got := cluster.Spec.ClusterNetwork.ServiceDomain; got != "cluster.local" {
		t.Errorf("ServiceDomain = %q, want cluster.local", got)
	}

	// Re-fetch to confirm the fields survive a round trip through the fake
	// client's scheme-based storage, not just the in-memory return value.
	fetched, err := c.GetCluster(context.Background(), "default", "timbernetes")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	if fetched.Spec.ClusterNetwork == nil || fetched.Spec.ClusterNetwork.ServiceDomain != "cluster.local" {
		t.Errorf("ClusterNetwork did not survive round trip: %+v", fetched.Spec.ClusterNetwork)
	}
}

func TestCreateClusterOmitsClusterNetworkWhenNotProvided(t *testing.T) {
	c := newFakeClient(t, newTestClusterClass("default", "capn-audit"))

	cluster, err := c.CreateCluster(context.Background(), CreateClusterOptions{
		Name:                 "no-network-cluster",
		Namespace:            "default",
		ClusterClass:         "capn-audit",
		KubernetesVersion:    "v1.35.0",
		ControlPlaneReplicas: 1,
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	if cluster.Spec.ClusterNetwork != nil {
		t.Errorf("expected Spec.ClusterNetwork to be nil, got %+v", cluster.Spec.ClusterNetwork)
	}
}

func TestCreateClusterSetsMachineDeploymentVariableOverrides(t *testing.T) {
	c := newFakeClient(t, newTestClusterClass("default", "capn-audit"))

	cluster, err := c.CreateCluster(context.Background(), CreateClusterOptions{
		Name:                 "timbernetes",
		Namespace:            "default",
		ClusterClass:         "capn-audit",
		KubernetesVersion:    "v1.35.0",
		ControlPlaneReplicas: 1,
		MachineDeployments: []MachineDeploymentSpec{
			{
				Class:    "default-worker",
				Name:     "md-0",
				Replicas: 2,
				Variables: map[string]interface{}{
					"instance": map[string]interface{}{
						"flavor": "c4-m8",
						"type":   "container",
					},
				},
			},
			{
				// No overrides: should not get a Variables block at all.
				Class:    "default-worker",
				Name:     "md-1",
				Replicas: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	mds := cluster.Spec.Topology.Workers.MachineDeployments
	if len(mds) != 2 {
		t.Fatalf("expected 2 machine deployments, got %d", len(mds))
	}

	md0 := mds[0]
	if md0.Variables == nil || len(md0.Variables.Overrides) != 1 {
		t.Fatalf("md-0: expected 1 variable override, got %+v", md0.Variables)
	}
	if md0.Variables.Overrides[0].Name != "instance" {
		t.Errorf("md-0: override name = %q, want instance", md0.Variables.Overrides[0].Name)
	}

	md1 := mds[1]
	if md1.Variables != nil {
		t.Errorf("md-1: expected no Variables block, got %+v", md1.Variables)
	}
}
