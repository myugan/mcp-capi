package capi

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
)

func newTopologyCluster(namespace, name string) *clusterv1.Cluster {
	controlPlaneReplicas := int32(1)
	workerReplicas := int32(1)
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{
				Class:   "capn-audit",
				Version: "v1.35.0",
				ControlPlane: clusterv1.ControlPlaneTopology{
					Replicas: &controlPlaneReplicas,
				},
				Workers: &clusterv1.WorkersTopology{
					MachineDeployments: []clusterv1.MachineDeploymentTopology{
						{Class: "default-worker", Name: "md-0", Replicas: &workerReplicas},
					},
				},
			},
		},
	}
}

func TestScaleClusterTopologyControlPlanePatchesTopologySpec(t *testing.T) {
	c := newFakeClient(t, newTopologyCluster("default", "timbernetes"))

	if err := c.ScaleCluster(context.Background(), "default", "timbernetes", "controlplane", 3, ""); err != nil {
		t.Fatalf("ScaleCluster failed: %v", err)
	}

	cluster, err := c.GetCluster(context.Background(), "default", "timbernetes")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	if got := cluster.Spec.Topology.ControlPlane.Replicas; got == nil || *got != 3 {
		t.Errorf("Topology.ControlPlane.Replicas = %v, want 3", got)
	}
}

func TestScaleClusterTopologyWorkersByTopologyName(t *testing.T) {
	c := newFakeClient(t, newTopologyCluster("default", "timbernetes"))

	if err := c.ScaleCluster(context.Background(), "default", "timbernetes", "workers", 5, "md-0"); err != nil {
		t.Fatalf("ScaleCluster failed: %v", err)
	}

	cluster, err := c.GetCluster(context.Background(), "default", "timbernetes")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	mds := cluster.Spec.Topology.Workers.MachineDeployments
	if len(mds) != 1 || mds[0].Replicas == nil || *mds[0].Replicas != 5 {
		t.Errorf("Topology.Workers.MachineDeployments = %+v, want replicas=5", mds)
	}
}

func TestScaleClusterTopologyWorkersByGeneratedResourceName(t *testing.T) {
	generatedMD := &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "timbernetes-md-0-wqd6g",
			Namespace: "default",
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:                          "timbernetes",
				clusterv1.ClusterTopologyMachineDeploymentNameLabel: "md-0",
			},
		},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: "timbernetes",
			Template: clusterv1.MachineTemplateSpec{
				Spec: clusterv1.MachineSpec{ClusterName: "timbernetes"},
			},
		},
	}

	c := newFakeClient(t, newTopologyCluster("default", "timbernetes"), generatedMD)

	// Pass the generated resource name, as returned by ListMachineDeployments,
	// rather than the topology name -- this must resolve to "md-0" via the
	// topology name label and patch the Cluster's topology spec, not the
	// MachineDeployment object directly.
	if err := c.ScaleCluster(context.Background(), "default", "timbernetes", "workers", 7, "timbernetes-md-0-wqd6g"); err != nil {
		t.Fatalf("ScaleCluster failed: %v", err)
	}

	cluster, err := c.GetCluster(context.Background(), "default", "timbernetes")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	mds := cluster.Spec.Topology.Workers.MachineDeployments
	if len(mds) != 1 || mds[0].Replicas == nil || *mds[0].Replicas != 7 {
		t.Errorf("Topology.Workers.MachineDeployments = %+v, want replicas=7", mds)
	}

	// The generated MachineDeployment itself must be left untouched -- the
	// topology controller owns reconciling it from the topology spec.
	fetchedMD, err := c.GetMachineDeployment(context.Background(), "default", "timbernetes-md-0-wqd6g")
	if err != nil {
		t.Fatalf("GetMachineDeployment failed: %v", err)
	}
	if fetchedMD.Spec.Replicas != nil {
		t.Errorf("MachineDeployment.Spec.Replicas = %v, want untouched (nil)", fetchedMD.Spec.Replicas)
	}
}

func TestScaleClusterTopologyWorkersUnknownMachineDeploymentErrors(t *testing.T) {
	c := newFakeClient(t, newTopologyCluster("default", "timbernetes"))

	err := c.ScaleCluster(context.Background(), "default", "timbernetes", "workers", 3, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown machine deployment, got nil")
	}
}

func TestScaleClusterNonTopologyClusterPatchesMachineDeploymentDirectly(t *testing.T) {
	replicas := int32(1)
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
	}
	md := &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-md-0", Namespace: "default"},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: "plain",
			Replicas:    &replicas,
			Template: clusterv1.MachineTemplateSpec{
				Spec: clusterv1.MachineSpec{ClusterName: "plain"},
			},
		},
	}
	c := newFakeClient(t, cluster, md)

	if err := c.ScaleCluster(context.Background(), "default", "plain", "workers", 4, "plain-md-0"); err != nil {
		t.Fatalf("ScaleCluster failed: %v", err)
	}

	fetchedMD, err := c.GetMachineDeployment(context.Background(), "default", "plain-md-0")
	if err != nil {
		t.Fatalf("GetMachineDeployment failed: %v", err)
	}
	if fetchedMD.Spec.Replicas == nil || *fetchedMD.Spec.Replicas != 4 {
		t.Errorf("MachineDeployment.Spec.Replicas = %v, want 4", fetchedMD.Spec.Replicas)
	}
}
