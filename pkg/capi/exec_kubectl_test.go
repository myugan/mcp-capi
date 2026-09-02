package capi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// workloadKubeconfig is a minimal but parseable kubeconfig for a fake
// workload cluster. Nothing ever connects to it: the stub kubectl below
// ignores it entirely.
const workloadKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: https://workload.example.com:6443
contexts:
- name: workload
  context:
    cluster: workload
    user: workload
current-context: workload
users:
- name: workload
  user:
    token: fake-token
`

// newKubectlTestClient builds a Client whose GetKubeconfig resolves for
// namespace/name, so ExecKubectl gets as far as actually running kubectl.
func newKubectlTestClient(t *testing.T, namespace, name string) *Client {
	t.Helper()

	scheme := k8sruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add client-go scheme: %v", err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add CAPI scheme: %v", err)
	}

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-kubeconfig", Namespace: namespace},
		Data:       map[string][]byte{"value": []byte(workloadKubeconfig)},
	}

	c := &Client{}
	c.SetClients(k8sfake.NewSimpleClientset(secret), ctrlClient)
	return c
}

// stubKubectl points kubectlBinaryPath at a script that echoes back what the
// real kubectl would have received: its own argv, then everything on stdin.
// The original path is restored via t.Cleanup.
func stubKubectl(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("stub kubectl script requires a POSIX shell")
	}

	script := filepath.Join(t.TempDir(), "kubectl")
	body := "#!/bin/sh\necho \"ARGS: $*\"\necho \"STDIN-BEGIN\"\ncat\necho \"STDIN-END\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("failed to write stub kubectl: %v", err)
	}

	original := kubectlBinaryPath
	kubectlBinaryPath = script
	t.Cleanup(func() { kubectlBinaryPath = original })
}

// TestExecKubectlPipesStdin is the regression test for "apply -f -": before
// stdin was wired into the subprocess, kubectl read a closed stdin and every
// such call failed.
func TestExecKubectlPipesStdin(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: mammoth\n"

	output, err := c.ExecKubectl(context.Background(), "default", "timbernetes",
		[]string{"apply", "-f", "-"}, "", manifest)
	if err != nil {
		t.Fatalf("ExecKubectl failed: %v (output: %s)", err, output)
	}

	if !strings.Contains(output, "ARGS: --kubeconfig") {
		t.Errorf("expected kubectl to be invoked with --kubeconfig, got: %s", output)
	}
	if !strings.Contains(output, "apply -f -") {
		t.Errorf("expected args to be forwarded verbatim, got: %s", output)
	}
	if !strings.Contains(output, "STDIN-BEGIN\n"+manifest+"STDIN-END") {
		t.Errorf("expected manifest on kubectl's stdin, got: %s", output)
	}
}

// TestExecKubectlEmptyStdinDoesNotHang covers the no-stdin path: cmd.Stdin
// stays nil, so kubectl reads an immediately-closed stdin instead of blocking.
func TestExecKubectlEmptyStdinDoesNotHang(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	output, err := c.ExecKubectl(context.Background(), "default", "timbernetes",
		[]string{"get", "pods", "-A"}, "", "")
	if err != nil {
		t.Fatalf("ExecKubectl failed: %v (output: %s)", err, output)
	}

	if !strings.Contains(output, "STDIN-BEGIN\nSTDIN-END") {
		t.Errorf("expected empty stdin, got: %s", output)
	}
}

// TestExecKubectlImpersonationFlag pins that --as is injected by the client
// ahead of the caller's args, since args cannot carry it.
func TestExecKubectlImpersonationFlag(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	output, err := c.ExecKubectl(context.Background(), "default", "timbernetes",
		[]string{"auth", "can-i", "--list"}, "grug@example.com", "")
	if err != nil {
		t.Fatalf("ExecKubectl failed: %v (output: %s)", err, output)
	}

	if !strings.Contains(output, "--as grug@example.com auth can-i --list") {
		t.Errorf("expected --as before caller args, got: %s", output)
	}
}

// TestExecKubectlRejectsBlockedFlags keeps the stdin and impersonation
// additions from widening the escape surface: target-overriding flags are
// still rejected before kubectl runs, in both --flag and --flag=value form.
func TestExecKubectlRejectsBlockedFlags(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	for _, args := range [][]string{
		{"apply", "-f", "-", "--kubeconfig", "/tmp/evil.yaml"},
		{"get", "pods", "--server=https://evil.example.com"},
		{"auth", "can-i", "--list", "--as", "system:masters"},
		{"get", "pods", "--token=stolen"},
	} {
		if _, err := c.ExecKubectl(context.Background(), "default", "timbernetes", args, "", "manifest"); err == nil {
			t.Errorf("expected %v to be rejected, got no error", args)
		}
	}
}

// TestExecKubectlRequiresArgs pins the empty-args guard.
func TestExecKubectlRequiresArgs(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	if _, err := c.ExecKubectl(context.Background(), "default", "timbernetes", nil, "", "manifest"); err == nil {
		t.Error("expected empty args to be rejected, got no error")
	}
}

// TestExecKubectlUnknownClusterNeverRunsKubectl pins that a kubeconfig
// resolution failure short-circuits before the subprocess starts.
func TestExecKubectlUnknownClusterNeverRunsKubectl(t *testing.T) {
	stubKubectl(t)
	c := newKubectlTestClient(t, "default", "timbernetes")

	output, err := c.ExecKubectl(context.Background(), "default", "no-such-cluster",
		[]string{"get", "pods"}, "", "")
	if err == nil {
		t.Fatal("expected unknown cluster to be rejected, got no error")
	}
	if strings.Contains(output, "ARGS:") {
		t.Errorf("kubectl should not have run, got: %s", output)
	}
}
