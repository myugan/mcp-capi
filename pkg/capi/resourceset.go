package capi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	addonsv1 "sigs.k8s.io/cluster-api/api/addons/v1beta1" //nolint:staticcheck // CAPI v1beta1 required until v1beta2 migration
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClusterResourceSetResourceSpec describes one Secret or ConfigMap resource
// referenced by a ClusterResourceSet. If Data is non-empty, the referenced
// resource is created (or updated, if it already exists) with that data
// before the ClusterResourceSet is created; otherwise a resource with this
// kind/name must already exist in the same namespace.
type ClusterResourceSetResourceSpec struct {
	// Kind is either "Secret" or "ConfigMap".
	Kind string
	Name string
	// Data holds the manifest(s) to apply to matching clusters, keyed by
	// filename (e.g. "cni.yaml"). Optional -- omit to reference an existing
	// resource instead of creating one.
	Data map[string]string
}

// CreateClusterResourceSetOptions contains options for creating a new
// ClusterResourceSet -- the mechanism CAPI uses to apply arbitrary manifests
// (most commonly a CNI) to every Cluster matching a label selector. This is
// the same pattern used for the flannel ConfigMap + ClusterResourceSet pair
// deployed alongside ClusterClass-based clusters in this environment (see
// the capn.cluster.x-k8s.io/deploy-kube-flannel cluster label).
type CreateClusterResourceSetOptions struct {
	Name      string
	Namespace string
	// ClusterSelector must match the labels on the target Cluster(s).
	ClusterSelector map[string]string
	// Strategy is "ApplyOnce" (default) or "Reconcile".
	Strategy  string
	Resources []ClusterResourceSetResourceSpec
}

// CreateClusterResourceSet creates a new ClusterResourceSet, optionally
// creating the Secret/ConfigMap resources it references along the way.
func (c *Client) CreateClusterResourceSet(ctx context.Context, opts CreateClusterResourceSetOptions) (*addonsv1.ClusterResourceSet, error) {
	if len(opts.ClusterSelector) == 0 {
		return nil, fmt.Errorf("cluster_selector must not be empty")
	}
	if len(opts.Resources) == 0 {
		return nil, fmt.Errorf("at least one resource is required")
	}

	strategy := opts.Strategy
	if strategy == "" {
		strategy = string(addonsv1.ClusterResourceSetStrategyApplyOnce)
	}
	if strategy != string(addonsv1.ClusterResourceSetStrategyApplyOnce) && strategy != string(addonsv1.ClusterResourceSetStrategyReconcile) {
		return nil, fmt.Errorf("invalid strategy %q: must be %q or %q", strategy,
			addonsv1.ClusterResourceSetStrategyApplyOnce, addonsv1.ClusterResourceSetStrategyReconcile)
	}

	refs := make([]addonsv1.ResourceRef, 0, len(opts.Resources))
	for _, res := range opts.Resources {
		switch addonsv1.ClusterResourceSetResourceKind(res.Kind) {
		case addonsv1.SecretClusterResourceSetResourceKind:
			if len(res.Data) > 0 {
				if err := c.createOrUpdateResourceSetSecret(ctx, opts.Namespace, res.Name, res.Data); err != nil {
					return nil, fmt.Errorf("failed to create resource secret %s: %w", res.Name, err)
				}
			}
		case addonsv1.ConfigMapClusterResourceSetResourceKind:
			if len(res.Data) > 0 {
				if err := c.createOrUpdateResourceSetConfigMap(ctx, opts.Namespace, res.Name, res.Data); err != nil {
					return nil, fmt.Errorf("failed to create resource configmap %s: %w", res.Name, err)
				}
			}
		default:
			return nil, fmt.Errorf("invalid resource kind %q for %q: must be %q or %q", res.Kind, res.Name,
				addonsv1.SecretClusterResourceSetResourceKind, addonsv1.ConfigMapClusterResourceSetResourceKind)
		}
		refs = append(refs, addonsv1.ResourceRef{Kind: res.Kind, Name: res.Name})
	}

	crs := &addonsv1.ClusterResourceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
		},
		Spec: addonsv1.ClusterResourceSetSpec{
			ClusterSelector: metav1.LabelSelector{MatchLabels: opts.ClusterSelector},
			Resources:       refs,
			Strategy:        strategy,
		},
	}

	if err := c.ctrlClient.Create(ctx, crs); err != nil {
		return nil, fmt.Errorf("failed to create cluster resource set: %w", err)
	}

	return crs, nil
}

// createOrUpdateResourceSetConfigMap creates the ConfigMap holding one
// ClusterResourceSet resource entry, updating it in place if it already
// exists.
func (c *Client) createOrUpdateResourceSetConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
	if err := c.ctrlClient.Create(ctx, cm); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing := &corev1.ConfigMap{}
		if err := c.ctrlClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
			return err
		}
		existing.Data = data
		if err := c.ctrlClient.Update(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

// createOrUpdateResourceSetSecret creates the Secret holding one
// ClusterResourceSet resource entry, updating it in place if it already
// exists. The Secret's type is forced to the value CAPI's CRS controller
// requires (addons.cluster.x-k8s.io/resource-set); anything else is silently
// rejected by that controller at apply time.
func (c *Client) createOrUpdateResourceSetSecret(ctx context.Context, namespace, name string, data map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		StringData: data,
		Type:       addonsv1.ClusterResourceSetSecretType,
	}
	if err := c.ctrlClient.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing := &corev1.Secret{}
		if err := c.ctrlClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
			return err
		}
		existing.StringData = data
		existing.Type = addonsv1.ClusterResourceSetSecretType
		if err := c.ctrlClient.Update(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

// ListClusterResourceSets lists ClusterResourceSets in the given namespace
// (all namespaces if empty).
func (c *Client) ListClusterResourceSets(ctx context.Context, namespace string) (*addonsv1.ClusterResourceSetList, error) {
	list := &addonsv1.ClusterResourceSetList{}
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := c.ctrlClient.List(ctx, list, opts...); err != nil {
		return nil, fmt.Errorf("failed to list cluster resource sets: %w", err)
	}
	return list, nil
}

// GetClusterResourceSet retrieves a specific ClusterResourceSet.
func (c *Client) GetClusterResourceSet(ctx context.Context, namespace, name string) (*addonsv1.ClusterResourceSet, error) {
	crs := &addonsv1.ClusterResourceSet{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := c.ctrlClient.Get(ctx, key, crs); err != nil {
		return nil, fmt.Errorf("failed to get cluster resource set %s/%s: %w", namespace, name, err)
	}
	return crs, nil
}

// DeleteClusterResourceSet deletes a ClusterResourceSet. The Secrets/ConfigMaps
// it referenced are left in place.
func (c *Client) DeleteClusterResourceSet(ctx context.Context, namespace, name string) error {
	crs, err := c.GetClusterResourceSet(ctx, namespace, name)
	if err != nil {
		return err
	}
	if err := c.ctrlClient.Delete(ctx, crs); err != nil {
		return fmt.Errorf("failed to delete cluster resource set: %w", err)
	}
	return nil
}
