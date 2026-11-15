package capi

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
)

func TestMachineNotReadyIssue(t *testing.T) {
	for name, tc := range map[string]struct {
		phase      string
		conditions clusterv1.Conditions
		want       string
	}{
		"the Ready condition explains itself": {
			phase: "Provisioning",
			conditions: clusterv1.Conditions{
				{Type: clusterv1.ReadyCondition, Status: corev1.ConditionFalse, Reason: "WaitingForBootstrapData", Message: "bootstrap secret not found"},
				{Type: "BootstrapReady", Status: corev1.ConditionFalse, Reason: "Other"},
			},
			want: "Machine m1 is not ready (phase Provisioning): Ready WaitingForBootstrapData: bootstrap secret not found",
		},
		"a bare Ready condition gives way to the condition behind it": {
			phase: "Provisioned",
			conditions: clusterv1.Conditions{
				{Type: clusterv1.ReadyCondition, Status: corev1.ConditionFalse},
				{Type: "BootstrapReady", Status: corev1.ConditionTrue},
				{Type: "NodeHealthy", Status: corev1.ConditionUnknown, Reason: "NodeProvisioning"},
			},
			want: "Machine m1 is not ready (phase Provisioned): NodeHealthy NodeProvisioning",
		},
		"no Ready condition, another one is not True": {
			conditions: clusterv1.Conditions{
				{Type: "InfrastructureReady", Status: corev1.ConditionFalse, Message: "instance create failed: quota exceeded"},
			},
			want: "Machine m1 is not ready: InfrastructureReady: instance create failed: quota exceeded",
		},
		"nothing to go on": {
			phase: "Pending",
			want:  "Machine m1 is not ready (phase Pending)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			machine := &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{Name: "m1"},
				Status:     clusterv1.MachineStatus{Phase: tc.phase, Conditions: tc.conditions},
			}
			if got := machineNotReadyIssue(machine); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestConditionIssueFallsBackToReason(t *testing.T) {
	for want, condition := range map[string]clusterv1.Condition{
		"Ready: etcd member unhealthy": {Type: "Ready", Status: corev1.ConditionFalse, Reason: "EtcdUnhealthy", Message: "etcd member unhealthy"},
		"Ready: EtcdUnhealthy":         {Type: "Ready", Status: corev1.ConditionFalse, Reason: "EtcdUnhealthy"},
		"Ready: status Unknown":        {Type: "Ready", Status: corev1.ConditionUnknown},
	} {
		if got := conditionIssue(condition); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestUserMetadataDropsSystemKeys(t *testing.T) {
	got := UserMetadata(map[string]string{
		"cluster.x-k8s.io/cluster-name":                    "c1",
		"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"Cluster"}`,
		"owner": "team-a",
	})
	if len(got) != 1 || got["owner"] != "team-a" {
		t.Fatalf("only the user's own key must stay, got %v", got)
	}
}
