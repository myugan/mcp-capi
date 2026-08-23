package capi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	addonsv1 "sigs.k8s.io/cluster-api/api/addons/v1beta1"                     //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1"                      //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Client provides access to CAPI resources in a Kubernetes cluster
type Client struct {
	// k8sClient is the standard Kubernetes client
	k8sClient kubernetes.Interface

	// ctrlClient is the controller-runtime client for CAPI resources
	ctrlClient client.Client

	// config is the rest config used to connect
	config *rest.Config
}

// NewClient creates a new CAPI client
func NewClient(kubeconfig string) (*Client, error) {
	config, err := loadConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create standard Kubernetes client
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Create controller-runtime client with CAPI scheme
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add CAPI to scheme: %w", err)
	}
	if err := addonsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add CAPI addons to scheme: %w", err)
	}

	ctrlClient, err := client.New(config, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller client: %w", err)
	}

	return &Client{
		k8sClient:  k8sClient,
		ctrlClient: ctrlClient,
		config:     config,
	}, nil
}

// loadConfig loads the kubeconfig from various sources
func loadConfig(kubeconfig string) (*rest.Config, error) {
	// If kubeconfig is provided, use it
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Try KUBECONFIG env var
	if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigEnv)
	}

	// Try default location
	if home := homedir.HomeDir(); home != "" {
		defaultPath := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(defaultPath); err == nil {
			return clientcmd.BuildConfigFromFlags("", defaultPath)
		}
	}

	return nil, fmt.Errorf("no kubeconfig found")
}

// GetK8sClient returns the standard Kubernetes client
func (c *Client) GetK8sClient() kubernetes.Interface {
	return c.k8sClient
}

// GetCtrlClient returns the controller-runtime client
func (c *Client) GetCtrlClient() client.Client {
	return c.ctrlClient
}

// SetClients sets the Kubernetes and controller-runtime clients (for testing)
func (c *Client) SetClients(k8sClient kubernetes.Interface, ctrlClient client.Client) {
	c.k8sClient = k8sClient
	c.ctrlClient = ctrlClient
}

// ListClusters lists all CAPI clusters in the given namespace
func (c *Client) ListClusters(ctx context.Context, namespace string, labelSelector map[string]string) (*clusterv1.ClusterList, error) {
	clusterList := &clusterv1.ClusterList{}

	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if len(labelSelector) > 0 {
		opts = append(opts, client.MatchingLabels(labelSelector))
	}

	if err := c.ctrlClient.List(ctx, clusterList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	return clusterList, nil
}

// FindClustersByLabelValue searches for clusters where any label value matches the given search term.
// This is useful when users refer to clusters by a human-friendly identifier stored in labels
// (e.g. a "friendly-name" or "cluster-id" label) rather than the Kubernetes resource name.
func (c *Client) FindClustersByLabelValue(ctx context.Context, namespace string, searchTerm string) (*clusterv1.ClusterList, error) {
	allClusters, err := c.ListClusters(ctx, namespace, nil)
	if err != nil {
		return nil, err
	}

	matched := &clusterv1.ClusterList{}
	for _, cluster := range allClusters.Items {
		for _, v := range cluster.Labels {
			if strings.EqualFold(v, searchTerm) {
				matched.Items = append(matched.Items, cluster)
				break
			}
		}
	}

	return matched, nil
}

// GetCluster retrieves a specific cluster
func (c *Client) GetCluster(ctx context.Context, namespace, name string) (*clusterv1.Cluster, error) {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return nil, fmt.Errorf("failed to get cluster %s/%s: %w", namespace, name, err)
	}

	return cluster, nil
}

// ListMachines lists all machines for a given cluster
func (c *Client) ListMachines(ctx context.Context, namespace, clusterName string) (*clusterv1.MachineList, error) {
	machineList := &clusterv1.MachineList{}

	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}

	if clusterName != "" {
		opts = append(opts, client.MatchingLabels{
			clusterv1.ClusterNameLabel: clusterName,
		})
	}

	if err := c.ctrlClient.List(ctx, machineList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list machines: %w", err)
	}

	return machineList, nil
}

// GetMachine retrieves a specific machine
func (c *Client) GetMachine(ctx context.Context, namespace, name string) (*clusterv1.Machine, error) {
	machine := &clusterv1.Machine{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, machine); err != nil {
		return nil, fmt.Errorf("failed to get machine %s/%s: %w", namespace, name, err)
	}

	return machine, nil
}

// DeleteMachineOptions contains options for deleting a machine
type DeleteMachineOptions struct {
	Namespace string
	Name      string
	Force     bool
}

// DeleteMachine deletes a CAPI machine
func (c *Client) DeleteMachine(ctx context.Context, opts DeleteMachineOptions) error {
	machine := &clusterv1.Machine{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	// First, get the machine to check if it exists
	if err := c.ctrlClient.Get(ctx, key, machine); err != nil {
		return fmt.Errorf("failed to get machine: %w", err)
	}

	// If not forcing, check if machine is safe to delete
	if !opts.Force {
		// Check if machine is healthy
		for _, condition := range machine.Status.Conditions {
			if condition.Type == clusterv1.MachineHealthCheckSucceededCondition && condition.Status == corev1.ConditionTrue {
				return fmt.Errorf("machine %s is healthy, use force=true to delete anyway", machine.Name)
			}
		}

		// Check if it's a control plane machine with only one replica
		if isControlPlaneMachine(machine) {
			// This is a simplified check - in production you'd want to check the actual replica count
			return fmt.Errorf("cannot delete control plane machine %s without force=true", machine.Name)
		}
	}

	// Delete the machine
	if err := c.ctrlClient.Delete(ctx, machine); err != nil {
		return fmt.Errorf("failed to delete machine: %w", err)
	}

	return nil
}

// RemediateMachineOptions contains options for remediating a machine
type RemediateMachineOptions struct {
	Namespace string
	Name      string
}

// RemediateMachine triggers machine health check remediation by annotating the machine
func (c *Client) RemediateMachine(ctx context.Context, opts RemediateMachineOptions) error {
	machine := &clusterv1.Machine{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	if err := c.ctrlClient.Get(ctx, key, machine); err != nil {
		return fmt.Errorf("failed to get machine: %w", err)
	}

	// Add remediation annotation
	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}
	machine.Annotations["cluster.x-k8s.io/remediate-machine"] = fmt.Sprintf("%d", time.Now().Unix())

	// Update the machine
	if err := c.ctrlClient.Update(ctx, machine); err != nil {
		return fmt.Errorf("failed to update machine with remediation annotation: %w", err)
	}

	return nil
}

// ListMachineDeployments lists all machine deployments
func (c *Client) ListMachineDeployments(ctx context.Context, namespace, clusterName string) (*clusterv1.MachineDeploymentList, error) {
	mdList := &clusterv1.MachineDeploymentList{}

	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	if clusterName != "" {
		opts = append(opts, client.MatchingLabels{
			clusterv1.ClusterNameLabel: clusterName,
		})
	}

	if err := c.ctrlClient.List(ctx, mdList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list machine deployments: %w", err)
	}

	return mdList, nil
}

// GetMachineDeployment retrieves a specific machine deployment
func (c *Client) GetMachineDeployment(ctx context.Context, namespace, name string) (*clusterv1.MachineDeployment, error) {
	md := &clusterv1.MachineDeployment{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, md); err != nil {
		return nil, fmt.Errorf("failed to get machine deployment: %w", err)
	}

	return md, nil
}

// GetKubeconfig retrieves the kubeconfig for a workload cluster
func (c *Client) GetKubeconfig(ctx context.Context, namespace, clusterName string) (string, error) {
	// Verify a real Cluster resource exists before trusting the guessed secret
	// name below. Without this, any caller could pass an arbitrary name and
	// retrieve whatever secret happens to exist at {name}-kubeconfig, which is
	// not necessarily a CAPI-managed workload cluster.
	if _, err := c.GetCluster(ctx, namespace, clusterName); err != nil {
		return "", fmt.Errorf("refusing to resolve kubeconfig: no such cluster %s/%s: %w", namespace, clusterName, err)
	}

	// The kubeconfig is typically stored in a secret named {cluster-name}-kubeconfig
	secretName := fmt.Sprintf("%s-kubeconfig", clusterName)

	secret, err := c.k8sClient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get kubeconfig secret: %w", err)
	}

	// The kubeconfig is typically stored in the 'value' key
	kubeconfigData, exists := secret.Data["value"]
	if !exists {
		// Try 'data' key as alternative
		kubeconfigData, exists = secret.Data["data"]
		if !exists {
			// List all keys for debugging
			var keys []string
			for k := range secret.Data {
				keys = append(keys, k)
			}
			return "", fmt.Errorf("kubeconfig not found in secret, available keys: %v", keys)
		}
	}

	return string(kubeconfigData), nil
}

// kubectlBinaryPath is the absolute path to the kubectl binary baked into the
// container image. An absolute path is used rather than relying on $PATH
// lookup, since the runtime image has no shell/environment to speak of.
const kubectlBinaryPath = "/usr/local/bin/kubectl"

// helmBinaryPath is the absolute path to the helm binary baked into the
// container image, for the same reason as kubectlBinaryPath above.
const helmBinaryPath = "/usr/local/bin/helm"

// blockedKubectlFlags are flags that would let a caller override the
// resolved workload-cluster target or otherwise escape it (pointing kubectl
// at a different context, server, or set of credentials than the one this
// function resolved). Any argument matching one of these is rejected before
// kubectl ever runs.
var blockedKubectlFlags = []string{
	"--kubeconfig", "--context", "--cluster", "--user",
	"--server", "-s", "--token", "--as", "--as-group", "--as-uid",
	"--client-certificate", "--client-key", "--certificate-authority",
}

// blockedHelmFlags are the Helm equivalents of blockedKubectlFlags: flags
// that would let a caller redirect helm at a different kube-apiserver,
// context, or set of credentials than the resolved workload cluster. Any
// argument matching one of these is rejected before helm ever runs.
var blockedHelmFlags = []string{
	"--kubeconfig", "--kube-context", "--kube-apiserver", "--kube-token",
	"--kube-as-user", "--kube-as-group", "--kube-ca-file",
	"--kube-insecure-skip-tls-verify", "--kube-tls-server-name",
}

// resolveWorkloadKubeconfigFile dynamically resolves the given workload
// cluster's kubeconfig and writes it to a private temp file for a CLI
// subprocess (kubectl/helm) to use via --kubeconfig. It never falls back to
// any default/in-cluster kubeconfig: if resolution fails, or if the resolved
// server host matches the management cluster's own API host, the call is
// refused. The returned cleanup func removes the temp file and must always
// be called.
func (c *Client) resolveWorkloadKubeconfigFile(ctx context.Context, namespace, clusterName string) (path string, cleanup func(), err error) {
	kubeconfig, err := c.GetKubeconfig(ctx, namespace, clusterName)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get kubeconfig for cluster %s: %w", clusterName, err)
	}

	workloadConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse kubeconfig for cluster %s: %w", clusterName, err)
	}

	// Defense in depth: even though GetKubeconfig already validated a real
	// Cluster resource exists, refuse to proceed if the resolved server
	// somehow matches the management cluster's own API host.
	if c.config != nil && c.config.Host != "" {
		mgmtHost, mgmtErr := url.Parse(c.config.Host)
		workloadHost, workloadErr := url.Parse(workloadConfig.Host)
		if mgmtErr == nil && workloadErr == nil && mgmtHost.Hostname() != "" &&
			mgmtHost.Hostname() == workloadHost.Hostname() {
			return "", nil, fmt.Errorf("refusing to resolve kubeconfig: resolved target for cluster %s matches the management cluster's own API host", clusterName)
		}
	}

	// os.CreateTemp creates the file with mode 0600, so the kubeconfig
	// (bearer tokens/client certs included) is never world- or group-readable.
	tmpFile, err := os.CreateTemp("", "capi-kubeconfig-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp kubeconfig file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup = func() { os.Remove(tmpPath) }

	if _, err := tmpFile.WriteString(kubeconfig); err != nil {
		tmpFile.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to write temp kubeconfig file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to close temp kubeconfig file: %w", err)
	}

	return tmpPath, cleanup, nil
}

// ExecKubectl dynamically resolves the given workload cluster's kubeconfig
// and runs a real kubectl invocation directly against that cluster's own API
// server. It never falls back to any default/in-cluster kubeconfig: if
// resolution fails, if any argument attempts to redirect kubectl at a
// different target, or if the resolved server host matches the management
// cluster's own API host, the call is refused before kubectl ever runs.
//
// impersonateAs, when non-empty, is passed as kubectl's --as flag (e.g. to
// test another user's RBAC against the resolved workload cluster). It is
// the only way to set --as: the flag remains in blockedKubectlFlags so it
// cannot be smuggled in through args.
func (c *Client) ExecKubectl(ctx context.Context, namespace, clusterName string, args []string, impersonateAs string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("at least one kubectl argument is required")
	}

	for _, a := range args {
		for _, blocked := range blockedKubectlFlags {
			if a == blocked || strings.HasPrefix(a, blocked+"=") {
				return "", fmt.Errorf("argument %q is not allowed: it would override the resolved target cluster", a)
			}
		}
	}

	tmpPath, cleanup, err := c.resolveWorkloadKubeconfigFile(ctx, namespace, clusterName)
	if err != nil {
		return "", err
	}
	defer cleanup()

	kubectlArgs := []string{"--kubeconfig", tmpPath}
	if impersonateAs != "" {
		kubectlArgs = append(kubectlArgs, "--as", impersonateAs)
	}
	kubectlArgs = append(kubectlArgs, args...)
	cmd := exec.CommandContext(ctx, kubectlBinaryPath, kubectlArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("kubectl command failed for cluster %s: %w", clusterName, err)
	}

	return string(output), nil
}

// ExecHelm dynamically resolves the given workload cluster's kubeconfig and
// runs a real helm invocation directly against that cluster's own API
// server -- the same trust model as ExecKubectl. It never falls back to any
// default/in-cluster kubeconfig: if resolution fails, if any argument
// attempts to redirect helm at a different target, or if the resolved
// server host matches the management cluster's own API host, the call is
// refused before helm ever runs.
func (c *Client) ExecHelm(ctx context.Context, namespace, clusterName string, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("at least one helm argument is required")
	}

	for _, a := range args {
		for _, blocked := range blockedHelmFlags {
			if a == blocked || strings.HasPrefix(a, blocked+"=") {
				return "", fmt.Errorf("argument %q is not allowed: it would override the resolved target cluster", a)
			}
		}
	}

	tmpPath, cleanup, err := c.resolveWorkloadKubeconfigFile(ctx, namespace, clusterName)
	if err != nil {
		return "", err
	}
	defer cleanup()

	helmArgs := append([]string{"--kubeconfig", tmpPath}, args...)
	cmd := exec.CommandContext(ctx, helmBinaryPath, helmArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("helm command failed for cluster %s: %w", clusterName, err)
	}

	return string(output), nil
}

// PauseCluster pauses reconciliation for a cluster by adding the cluster.x-k8s.io/paused annotation
func (c *Client) PauseCluster(ctx context.Context, namespace, name string) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Add paused annotation
	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations[clusterv1.PausedAnnotation] = "true"

	if err := c.ctrlClient.Update(ctx, cluster); err != nil {
		return fmt.Errorf("failed to pause cluster: %w", err)
	}

	return nil
}

// ResumeCluster resumes reconciliation for a cluster by removing the cluster.x-k8s.io/paused annotation
func (c *Client) ResumeCluster(ctx context.Context, namespace, name string) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Remove paused annotation
	if cluster.Annotations != nil {
		delete(cluster.Annotations, clusterv1.PausedAnnotation)
	}

	if err := c.ctrlClient.Update(ctx, cluster); err != nil {
		return fmt.Errorf("failed to resume cluster: %w", err)
	}

	return nil
}

// DeleteCluster deletes a CAPI cluster
func (c *Client) DeleteCluster(ctx context.Context, namespace, name string) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Delete the cluster
	if err := c.ctrlClient.Delete(ctx, cluster); err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	return nil
}

// MachineDeploymentSpec describes one worker machine deployment topology to
// include when creating a cluster from a ClusterClass.
type MachineDeploymentSpec struct {
	// Class is the MachineDeploymentClass name defined in the ClusterClass.
	Class string
	// Name is the unique identifier for this MachineDeploymentTopology.
	Name string
	// Replicas is the number of worker nodes for this machine deployment.
	Replicas int32
	// Variables overrides Cluster-level topology variables for this
	// MachineDeployment specifically (e.g. a different instance flavor/image
	// for workers than the control plane). Optional; names and shapes must
	// match what the ClusterClass's MachineDeploymentClass variable schema
	// defines.
	Variables map[string]interface{}
}

// ClusterNetworkSpec describes spec.clusterNetwork -- the pod/service CIDR
// ranges and service domain for a Cluster. This is independent of
// ClusterClass topology variables (ClusterClasses do not set it), so it must
// be provided explicitly whenever the cluster's CNI/kube-proxy expect
// specific ranges (e.g. flannel's default 10.244.0.0/16 pod CIDR).
type ClusterNetworkSpec struct {
	Pods          []string
	Services      []string
	ServiceDomain string
}

// CreateClusterOptions contains options for creating a new cluster from an
// existing ClusterClass, via spec.topology -- the same mechanism used by
// every ClusterClass-managed cluster in this environment (e.g. timbernetes).
type CreateClusterOptions struct {
	Name                 string
	Namespace            string
	ClusterClass         string
	KubernetesVersion    string
	ControlPlaneReplicas int32
	MachineDeployments   []MachineDeploymentSpec
	// Variables are passed through as the Cluster's topology variables.
	// Their names and shapes must match what the referenced ClusterClass
	// defines; the API server validates them against the ClusterClass's
	// variable schemas on create.
	Variables map[string]interface{}
	// ClusterNetwork sets spec.clusterNetwork. Optional -- omit to leave it
	// unset (no pod/service CIDR configured on the Cluster object).
	ClusterNetwork *ClusterNetworkSpec
}

// toClusterVariables converts a variables map (as accepted by the
// capi_create_cluster tool for both Cluster-level and per-MachineDeployment
// overrides) into the []ClusterVariable form the API expects.
func toClusterVariables(vars map[string]interface{}) ([]clusterv1.ClusterVariable, error) {
	out := make([]clusterv1.ClusterVariable, 0, len(vars))
	for name, value := range vars {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal value for variable %s: %w", name, err)
		}
		out = append(out, clusterv1.ClusterVariable{
			Name:  name,
			Value: apiextensionsv1.JSON{Raw: raw},
		})
	}
	return out, nil
}

// CreateCluster creates a new CAPI cluster from an existing ClusterClass. It
// does not create the ClusterClass itself, nor any of the underlying control
// plane/infrastructure objects -- those are generated by CAPI's topology
// controller from the referenced ClusterClass.
func (c *Client) CreateCluster(ctx context.Context, opts CreateClusterOptions) (*clusterv1.Cluster, error) {
	// Verify the ClusterClass actually exists before submitting, for a clear
	// error instead of a cryptic admission failure.
	clusterClass := &clusterv1.ClusterClass{}
	ccKey := client.ObjectKey{Namespace: opts.Namespace, Name: opts.ClusterClass}
	if err := c.ctrlClient.Get(ctx, ccKey, clusterClass); err != nil {
		return nil, fmt.Errorf("cluster class %s/%s not found: %w", opts.Namespace, opts.ClusterClass, err)
	}

	variables, err := toClusterVariables(opts.Variables)
	if err != nil {
		return nil, err
	}

	machineDeployments := make([]clusterv1.MachineDeploymentTopology, 0, len(opts.MachineDeployments))
	for _, md := range opts.MachineDeployments {
		replicas := md.Replicas
		topology := clusterv1.MachineDeploymentTopology{
			Class:    md.Class,
			Name:     md.Name,
			Replicas: &replicas,
		}
		if len(md.Variables) > 0 {
			overrides, err := toClusterVariables(md.Variables)
			if err != nil {
				return nil, fmt.Errorf("machine deployment %s: %w", md.Name, err)
			}
			topology.Variables = &clusterv1.MachineDeploymentVariables{Overrides: overrides}
		}
		machineDeployments = append(machineDeployments, topology)
	}

	var clusterNetwork *clusterv1.ClusterNetwork
	if opts.ClusterNetwork != nil {
		clusterNetwork = &clusterv1.ClusterNetwork{
			ServiceDomain: opts.ClusterNetwork.ServiceDomain,
		}
		if len(opts.ClusterNetwork.Pods) > 0 {
			clusterNetwork.Pods = &clusterv1.NetworkRanges{CIDRBlocks: opts.ClusterNetwork.Pods}
		}
		if len(opts.ClusterNetwork.Services) > 0 {
			clusterNetwork.Services = &clusterv1.NetworkRanges{CIDRBlocks: opts.ClusterNetwork.Services}
		}
	}

	controlPlaneReplicas := opts.ControlPlaneReplicas
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
		},
		Spec: clusterv1.ClusterSpec{
			ClusterNetwork: clusterNetwork,
			Topology: &clusterv1.Topology{
				Class:   opts.ClusterClass,
				Version: opts.KubernetesVersion,
				ControlPlane: clusterv1.ControlPlaneTopology{
					Replicas: &controlPlaneReplicas,
				},
				Workers: &clusterv1.WorkersTopology{
					MachineDeployments: machineDeployments,
				},
				Variables: variables,
			},
		},
	}

	if err := c.ctrlClient.Create(ctx, cluster); err != nil {
		return nil, fmt.Errorf("failed to create cluster: %w", err)
	}

	return cluster, nil
}

// UpgradeClusterOptions contains options for upgrading a cluster
type UpgradeClusterOptions struct {
	Namespace      string
	Name           string
	TargetVersion  string
	UpgradeWorkers bool
}

// UpgradeCluster upgrades a CAPI cluster to a new Kubernetes version
func (c *Client) UpgradeCluster(ctx context.Context, opts UpgradeClusterOptions) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Update the control plane version
	if cluster.Spec.ControlPlaneRef != nil {
		switch cluster.Spec.ControlPlaneRef.Kind {
		case "KubeadmControlPlane":
			kcp := &controlplanev1.KubeadmControlPlane{}
			cpKey := client.ObjectKey{
				Namespace: cluster.Spec.ControlPlaneRef.Namespace,
				Name:      cluster.Spec.ControlPlaneRef.Name,
			}
			if err := c.ctrlClient.Get(ctx, cpKey, kcp); err != nil {
				return fmt.Errorf("failed to get control plane: %w", err)
			}

			// Update version
			kcp.Spec.Version = opts.TargetVersion
			if err := c.ctrlClient.Update(ctx, kcp); err != nil {
				return fmt.Errorf("failed to update control plane version: %w", err)
			}
		default:
			return fmt.Errorf("unsupported control plane type: %s", cluster.Spec.ControlPlaneRef.Kind)
		}
	}

	// Update worker nodes if requested
	if opts.UpgradeWorkers {
		mdList, err := c.ListMachineDeployments(ctx, opts.Namespace, opts.Name)
		if err != nil {
			return fmt.Errorf("failed to list machine deployments: %w", err)
		}

		for i := range mdList.Items {
			md := &mdList.Items[i]
			if md.Spec.Template.Spec.Version != nil {
				*md.Spec.Template.Spec.Version = opts.TargetVersion
				if err := c.ctrlClient.Update(ctx, md); err != nil {
					return fmt.Errorf("failed to update machine deployment %s: %w", md.Name, err)
				}
			}
		}
	}

	return nil
}

// UpdateClusterOptions contains options for updating a cluster
type UpdateClusterOptions struct {
	Namespace   string
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

// UpdateCluster updates a CAPI cluster's metadata
func (c *Client) UpdateCluster(ctx context.Context, opts UpdateClusterOptions) (*clusterv1.Cluster, error) {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Update labels
	if opts.Labels != nil {
		if cluster.Labels == nil {
			cluster.Labels = make(map[string]string)
		}
		for k, v := range opts.Labels {
			if v == "" {
				// Empty value means remove the label
				delete(cluster.Labels, k)
			} else {
				cluster.Labels[k] = v
			}
		}
	}

	// Update annotations
	if opts.Annotations != nil {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		for k, v := range opts.Annotations {
			if v == "" {
				// Empty value means remove the annotation
				delete(cluster.Annotations, k)
			} else {
				cluster.Annotations[k] = v
			}
		}
	}

	if err := c.ctrlClient.Update(ctx, cluster); err != nil {
		return nil, fmt.Errorf("failed to update cluster: %w", err)
	}

	return cluster, nil
}

// MoveClusterOptions contains options for moving a cluster
type MoveClusterOptions struct {
	Namespace        string
	Name             string
	TargetKubeconfig string
	TargetNamespace  string
	DryRun           bool
}

// MoveCluster prepares a cluster for migration to another management cluster
// Note: This is a simplified implementation that exports the cluster resources
func (c *Client) MoveCluster(ctx context.Context, opts MoveClusterOptions) (string, error) {
	// Get the cluster
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return "", fmt.Errorf("failed to get cluster: %w", err)
	}

	// Prepare target namespace
	targetNs := opts.TargetNamespace
	if targetNs == "" {
		targetNs = opts.Namespace
	}

	// Create a YAML manifest for the move
	var manifest strings.Builder
	manifest.WriteString("# Cluster Move Manifest\n")
	fmt.Fprintf(&manifest, "# Source: %s/%s\n", opts.Namespace, opts.Name)
	fmt.Fprintf(&manifest, "# Target: %s/%s\n", targetNs, opts.Name)
	manifest.WriteString("# Apply this manifest to the target management cluster\n")
	manifest.WriteString("---\n")

	// Note: In a real implementation, you would:
	// 1. Use clusterctl move command or equivalent
	// 2. Export all related resources (Machines, MachineDeployments, etc.)
	// 3. Handle infrastructure-specific resources
	// 4. Pause source cluster before move
	// 5. Update object references

	manifest.WriteString("# This is a placeholder implementation\n")
	manifest.WriteString("# In production, use 'clusterctl move' command\n")
	manifest.WriteString("# Example: clusterctl move --to-kubeconfig=target.kubeconfig\n")

	return manifest.String(), nil
}

// BackupClusterOptions contains options for backing up a cluster
type BackupClusterOptions struct {
	Namespace      string
	Name           string
	IncludeSecrets bool
	OutputFormat   string // yaml or json
}

// BackupCluster creates a backup of cluster resources
func (c *Client) BackupCluster(ctx context.Context, opts BackupClusterOptions) (string, error) {
	// Get the cluster
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: opts.Namespace,
		Name:      opts.Name,
	}

	if err := c.ctrlClient.Get(ctx, key, cluster); err != nil {
		return "", fmt.Errorf("failed to get cluster: %w", err)
	}

	// Create backup manifest
	var backup strings.Builder
	backup.WriteString("# Cluster Backup\n")
	fmt.Fprintf(&backup, "# Cluster: %s/%s\n", opts.Namespace, opts.Name)
	fmt.Fprintf(&backup, "# Date: %v\n", cluster.CreationTimestamp)
	backup.WriteString("# Resources included:\n")
	backup.WriteString("# - Cluster\n")
	backup.WriteString("# - Control Plane\n")
	backup.WriteString("# - MachineDeployments\n")
	backup.WriteString("# - Infrastructure Resources\n")
	if opts.IncludeSecrets {
		backup.WriteString("# - Secrets (kubeconfig, certificates)\n")
	}
	backup.WriteString("---\n")

	// Note: In a real implementation, you would:
	// 1. Export the Cluster resource
	// 2. Export ControlPlane resources
	// 3. Export all Machines and MachineDeployments
	// 4. Export infrastructure-specific resources
	// 5. Optionally export secrets (kubeconfig, certs)
	// 6. Add restore instructions

	backup.WriteString("# This is a placeholder implementation\n")
	backup.WriteString("# Use velero or similar tools for complete cluster backup\n")
	backup.WriteString("# Example: velero backup create cluster-backup --include-namespaces=<namespace>\n")

	return backup.String(), nil
}

// ClusterHealthStatus represents the health status of a cluster
type ClusterHealthStatus struct {
	Healthy           bool
	ControlPlaneReady bool
	WorkersReady      bool
	InfraReady        bool
	Issues            []string
	Warnings          []string
}

// GetClusterHealth checks the health of a cluster
func (c *Client) GetClusterHealth(ctx context.Context, namespace, name string) (*ClusterHealthStatus, error) {
	status, err := c.GetClusterStatus(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster status: %w", err)
	}

	health := &ClusterHealthStatus{
		Healthy:           true,
		ControlPlaneReady: status.ControlPlaneReady,
		InfraReady:        status.InfraReady,
		Issues:            []string{},
		Warnings:          []string{},
	}

	// Check control plane
	if !status.ControlPlaneReady {
		health.Healthy = false
		health.Issues = append(health.Issues, "Control plane is not ready")
	}

	// Check infrastructure
	if !status.InfraReady {
		health.Healthy = false
		health.Issues = append(health.Issues, "Infrastructure is not ready")
	}

	// Check workers
	machines, err := c.ListMachines(ctx, namespace, name)
	if err == nil {
		readyMachines := 0
		totalMachines := len(machines.Items)

		for _, machine := range machines.Items {
			for _, condition := range machine.Status.Conditions {
				if condition.Type == clusterv1.ReadyCondition && condition.Status == corev1.ConditionTrue {
					readyMachines++
					break
				}
			}
		}

		health.WorkersReady = readyMachines == totalMachines && totalMachines > 0
		if !health.WorkersReady {
			health.Healthy = false
			health.Issues = append(health.Issues, fmt.Sprintf("Only %d/%d machines are ready", readyMachines, totalMachines))
		}
	}

	// Check conditions for issues
	for _, condition := range status.Conditions {
		if condition.Status != corev1.ConditionTrue && condition.Severity == clusterv1.ConditionSeverityError {
			health.Healthy = false
			health.Issues = append(health.Issues, fmt.Sprintf("%s: %s", condition.Type, condition.Message))
		} else if condition.Status != corev1.ConditionTrue && condition.Severity == clusterv1.ConditionSeverityWarning {
			health.Warnings = append(health.Warnings, fmt.Sprintf("%s: %s", condition.Type, condition.Message))
		}
	}

	// Check phase
	if status.Phase != "Provisioned" && status.Phase != "" {
		health.Warnings = append(health.Warnings, fmt.Sprintf("Cluster phase is '%s', expected 'Provisioned'", status.Phase))
	}

	return health, nil
}

// CreateMachineDeploymentOptions contains options for creating a machine deployment
type CreateMachineDeploymentOptions struct {
	Namespace          string
	Name               string
	ClusterName        string
	Replicas           int32
	InfrastructureRef  corev1.ObjectReference
	BootstrapConfigRef corev1.ObjectReference
	Version            string
	Labels             map[string]string
	NodeDrainTimeout   *metav1.Duration
	MinReadySeconds    int32
}

// CreateMachineDeployment creates a new CAPI MachineDeployment
func (c *Client) CreateMachineDeployment(ctx context.Context, opts CreateMachineDeploymentOptions) (*clusterv1.MachineDeployment, error) {
	// Create the machine deployment
	md := &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    opts.Labels,
		},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: opts.ClusterName,
			Replicas:    &opts.Replicas,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"machinedeployment": opts.Name,
				},
			},
			Template: clusterv1.MachineTemplateSpec{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"machinedeployment":                  opts.Name,
						clusterv1.ClusterNameLabel:           opts.ClusterName,
						clusterv1.MachineDeploymentNameLabel: opts.Name,
					},
				},
				Spec: clusterv1.MachineSpec{
					ClusterName:       opts.ClusterName,
					Version:           &opts.Version,
					InfrastructureRef: opts.InfrastructureRef,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: &opts.BootstrapConfigRef,
					},
				},
			},
			MinReadySeconds: &opts.MinReadySeconds,
		},
	}

	if opts.NodeDrainTimeout != nil {
		md.Spec.Template.Spec.NodeDrainTimeout = opts.NodeDrainTimeout
	}

	// Create the machine deployment
	if err := c.ctrlClient.Create(ctx, md); err != nil {
		return nil, fmt.Errorf("failed to create machine deployment: %w", err)
	}

	return md, nil
}

// ScaleClusterOptions contains options for scaling a cluster
// ... existing code ...

// UpdateMachineDeploymentOptions contains options for updating a machine deployment
type UpdateMachineDeploymentOptions struct {
	Namespace        string
	Name             string
	Version          *string
	Replicas         *int32
	Labels           map[string]string
	Annotations      map[string]string
	MinReadySeconds  *int32
	NodeDrainTimeout *metav1.Duration
}

// UpdateMachineDeployment updates a MachineDeployment's configuration
func (c *Client) UpdateMachineDeployment(ctx context.Context, opts UpdateMachineDeploymentOptions) (*clusterv1.MachineDeployment, error) {
	md, err := c.GetMachineDeployment(ctx, opts.Namespace, opts.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get machine deployment: %w", err)
	}

	// Update version if specified
	if opts.Version != nil {
		md.Spec.Template.Spec.Version = opts.Version
	}

	// Update replicas if specified
	if opts.Replicas != nil {
		md.Spec.Replicas = opts.Replicas
	}

	// Update minReadySeconds if specified
	if opts.MinReadySeconds != nil {
		md.Spec.MinReadySeconds = opts.MinReadySeconds
	}

	// Update nodeDrainTimeout if specified
	if opts.NodeDrainTimeout != nil {
		md.Spec.Template.Spec.NodeDrainTimeout = opts.NodeDrainTimeout
	}

	// Update labels
	if opts.Labels != nil {
		if md.Labels == nil {
			md.Labels = make(map[string]string)
		}
		for k, v := range opts.Labels {
			if v == "" {
				delete(md.Labels, k)
			} else {
				md.Labels[k] = v
			}
		}
	}

	// Update annotations
	if opts.Annotations != nil {
		if md.Annotations == nil {
			md.Annotations = make(map[string]string)
		}
		for k, v := range opts.Annotations {
			if v == "" {
				delete(md.Annotations, k)
			} else {
				md.Annotations[k] = v
			}
		}
	}

	if err := c.ctrlClient.Update(ctx, md); err != nil {
		return nil, fmt.Errorf("failed to update machine deployment: %w", err)
	}

	return md, nil
}

// RolloutMachineDeploymentOptions contains options for triggering a rollout
type RolloutMachineDeploymentOptions struct {
	Namespace string
	Name      string
	Reason    string
}

// RolloutMachineDeployment triggers a rolling update of a MachineDeployment
func (c *Client) RolloutMachineDeployment(ctx context.Context, opts RolloutMachineDeploymentOptions) error {
	md, err := c.GetMachineDeployment(ctx, opts.Namespace, opts.Name)
	if err != nil {
		return fmt.Errorf("failed to get machine deployment: %w", err)
	}

	// Trigger rollout by updating an annotation
	if md.Spec.Template.Annotations == nil {
		md.Spec.Template.Annotations = make(map[string]string)
	}

	// Add rollout annotation with timestamp
	md.Spec.Template.Annotations["cluster.x-k8s.io/rollout-triggered"] = fmt.Sprintf("%v", metav1.Now().Unix())
	if opts.Reason != "" {
		md.Spec.Template.Annotations["cluster.x-k8s.io/rollout-reason"] = opts.Reason
	}

	if err := c.ctrlClient.Update(ctx, md); err != nil {
		return fmt.Errorf("failed to trigger rollout: %w", err)
	}

	return nil
}

// ListMachineSets lists all MachineSets in a namespace
func (c *Client) ListMachineSets(ctx context.Context, namespace, clusterName string) (*clusterv1.MachineSetList, error) {
	msList := &clusterv1.MachineSetList{}

	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	// Filter by cluster if specified
	if clusterName != "" {
		opts = append(opts, client.MatchingLabels{
			clusterv1.ClusterNameLabel: clusterName,
		})
	}

	if err := c.ctrlClient.List(ctx, msList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list machine sets: %w", err)
	}

	return msList, nil
}

// GetMachineSet retrieves a specific MachineSet
func (c *Client) GetMachineSet(ctx context.Context, namespace, name string) (*clusterv1.MachineSet, error) {
	ms := &clusterv1.MachineSet{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.ctrlClient.Get(ctx, key, ms); err != nil {
		return nil, fmt.Errorf("failed to get machine set %s/%s: %w", namespace, name, err)
	}

	return ms, nil
}

// NodeOperationOptions contains options for node operations
type NodeOperationOptions struct {
	Namespace   string
	MachineName string
	NodeName    string
	// For drain operations
	GracePeriodSeconds *int32
	IgnoreDaemonSets   bool
	DeleteLocalData    bool
	Force              bool
	// For cordon operations
	Uncordon bool
}

// DrainNode safely drains a node
func (c *Client) DrainNode(ctx context.Context, opts NodeOperationOptions) error {
	// Get the node name from machine if not provided
	nodeName := opts.NodeName
	if nodeName == "" && opts.MachineName != "" {
		machine, err := c.GetMachine(ctx, opts.Namespace, opts.MachineName)
		if err != nil {
			return fmt.Errorf("failed to get machine: %w", err)
		}
		if machine.Status.NodeRef == nil {
			return fmt.Errorf("machine %s has no associated node", opts.MachineName)
		}
		nodeName = machine.Status.NodeRef.Name
	}

	if nodeName == "" {
		return fmt.Errorf("either nodeName or machineName must be provided")
	}

	// First cordon the node
	node, err := c.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Mark as unschedulable
	node.Spec.Unschedulable = true
	if _, err := c.k8sClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
	}

	// TODO: Implement actual pod eviction logic
	// This would involve:
	// 1. List all pods on the node
	// 2. Filter out daemonsets if IgnoreDaemonSets is true
	// 3. Create eviction objects for each pod
	// 4. Wait for pods to be evicted

	// For now, return a placeholder message
	return fmt.Errorf("drain operation not fully implemented - node %s has been cordoned", nodeName)
}

// CordonNode cordons or uncordons a node
func (c *Client) CordonNode(ctx context.Context, opts NodeOperationOptions) error {
	// Get the node name from machine if not provided
	nodeName := opts.NodeName
	if nodeName == "" && opts.MachineName != "" {
		machine, err := c.GetMachine(ctx, opts.Namespace, opts.MachineName)
		if err != nil {
			return fmt.Errorf("failed to get machine: %w", err)
		}
		if machine.Status.NodeRef == nil {
			return fmt.Errorf("machine %s has no associated node", opts.MachineName)
		}
		nodeName = machine.Status.NodeRef.Name
	}

	if nodeName == "" {
		return fmt.Errorf("either nodeName or machineName must be provided")
	}

	node, err := c.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Update schedulable status
	node.Spec.Unschedulable = !opts.Uncordon

	if _, err := c.k8sClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update node %s: %w", nodeName, err)
	}

	return nil
}

// GetNodeStatus gets the status of a node in the workload cluster
func (c *Client) GetNodeStatus(ctx context.Context, opts NodeOperationOptions) (*corev1.Node, error) {
	// Get the node name from machine if not provided
	nodeName := opts.NodeName
	if nodeName == "" && opts.MachineName != "" {
		machine, err := c.GetMachine(ctx, opts.Namespace, opts.MachineName)
		if err != nil {
			return nil, fmt.Errorf("failed to get machine: %w", err)
		}
		if machine.Status.NodeRef == nil {
			return nil, fmt.Errorf("machine %s has no associated node", opts.MachineName)
		}
		nodeName = machine.Status.NodeRef.Name
	}

	if nodeName == "" {
		return nil, fmt.Errorf("either nodeName or machineName must be provided")
	}

	// Get node from workload cluster
	// Note: This uses the management cluster client. In a real implementation,
	// you would need to get the workload cluster kubeconfig and create a client for it
	node, err := c.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	return node, nil
}

// isControlPlaneMachine checks if a machine is a control plane machine.
// This is a v1beta1-compatible helper that replaces util.IsControlPlaneMachine
// which now requires v1beta2 types.
func isControlPlaneMachine(machine *clusterv1.Machine) bool {
	if machine.Labels == nil {
		return false
	}
	_, exists := machine.Labels[clusterv1.MachineControlPlaneLabel]
	return exists
}
