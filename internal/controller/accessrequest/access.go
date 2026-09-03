package accessrequest

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusteraccess"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	"github.com/openmcp-project/controller-utils/pkg/pairs"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"

	"github.com/openmcp-project/cluster-provider-k3d/api/v1alpha1"
	"github.com/openmcp-project/cluster-provider-k3d/internal/controller/cluster"
)

var (
	// managedByNameLabel and managedByNamespaceLabel mark resources in the
	// target cluster as managed by an AccessRequest.
	managedByNameLabel      = v1alpha1.GroupVersion.Group + "/managed-by-name"
	managedByNamespaceLabel = v1alpha1.GroupVersion.Group + "/managed-by-namespace"
)

const (
	// serviceAccountNamespace is the namespace in the target cluster that
	// holds the serviceaccounts created for token-based access.
	serviceAccountNamespace = "accessrequests"

	// tokenValidity is the requested serviceaccount token lifetime.
	tokenValidity = 30 * 24 * time.Hour
	// refreshRatio is the fraction of the token lifetime after which the
	// token is renewed.
	refreshRatio = 0.8
)

func (r *AccessRequestReconciler) handleCreateOrUpdate(ctx context.Context, ar *clustersv1alpha1.AccessRequest, cl *clustersv1alpha1.Cluster) (reconcile.Result, error) {
	if controllerutil.AddFinalizer(ar, cluster.Finalizer) {
		if err := r.platformCluster.Client().Update(ctx, ar); err != nil {
			return reconcile.Result{}, fmt.Errorf("error adding finalizer: %w", err)
		}
	}

	name := cluster.K3dName(cl)

	if ar.Spec.Token == nil {
		return reconcile.Result{}, r.reconcileAdminAccess(ctx, name, ar)
	}

	if requeueAfter, ok := tokenStillValid(ctx, r.platformCluster.Client(), ar); ok {
		return reconcile.Result{RequeueAfter: requeueAfter}, nil
	}
	return r.reconcileTokenAccess(ctx, name, ar)
}

func (r *AccessRequestReconciler) handleDelete(ctx context.Context, ar *clustersv1alpha1.AccessRequest, cl *clustersv1alpha1.Cluster) error {
	if !controllerutil.ContainsFinalizer(ar, cluster.Finalizer) {
		return nil
	}

	// If the k3d cluster is already gone, there is nothing to clean up in it.
	name := cluster.K3dName(cl)
	exists, err := r.provider.ClusterExists(ctx, name)
	if err != nil {
		return fmt.Errorf("error checking for k3d cluster %q: %w", name, err)
	}
	if exists {
		targetClient, _, err := r.targetClient(ctx, name)
		if err != nil {
			return err
		}
		if err := cleanupResources(ctx, targetClient, nil, managedResourcesLabels(ar)); err != nil {
			return err
		}
	}

	// The kubeconfig secret is deleted by garbage collection via owner reference.
	controllerutil.RemoveFinalizer(ar, cluster.Finalizer)
	if err := r.platformCluster.Client().Update(ctx, ar); err != nil {
		return fmt.Errorf("error removing finalizer: %w", err)
	}
	return nil
}

// reconcileAdminAccess stores the admin kubeconfig of the target cluster in the AccessRequest's secret.
func (r *AccessRequestReconciler) reconcileAdminAccess(ctx context.Context, name string, ar *clustersv1alpha1.AccessRequest) error {
	kubeconfig, err := r.provider.Kubeconfig(ctx, name, true)
	if err != nil {
		return fmt.Errorf("error getting kubeconfig for k3d cluster %q: %w", name, err)
	}
	return r.writeAccessSecret(ctx, ar, map[string][]byte{
		clustersv1alpha1.SecretKeyKubeconfig: []byte(kubeconfig),
	})
}

// reconcileTokenAccess creates a serviceaccount with the requested
// permissions in the target cluster and stores a token kubeconfig in
// the AccessRequest's secret.
func (r *AccessRequestReconciler) reconcileTokenAccess(ctx context.Context, name string, ar *clustersv1alpha1.AccessRequest) (reconcile.Result, error) {
	targetClient, restCfg, err := r.targetClient(ctx, name)
	if err != nil {
		return reconcile.Result{}, err
	}

	if _, err := clusteraccess.EnsureNamespace(ctx, targetClient, serviceAccountNamespace); err != nil {
		return reconcile.Result{}, fmt.Errorf("error ensuring namespace %q: %w", serviceAccountNamespace, err)
	}

	expectedLabels := pairs.MapToPairs(managedResourcesLabels(ar))
	saName := ctrlutils.NameHashSHAKE128Base32(r.environment, r.providerName, ar.Namespace, ar.Name)
	sa, err := clusteraccess.EnsureServiceAccount(ctx, targetClient, saName, serviceAccountNamespace, expectedLabels...)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error ensuring serviceaccount %q/%q: %w", serviceAccountNamespace, saName, err)
	}

	keep, err := r.ensurePermissions(ctx, targetClient, sa, ar)
	if err != nil {
		return reconcile.Result{}, err
	}
	keep = append(keep, sa)

	token, err := clusteraccess.CreateTokenForServiceAccount(ctx, targetClient, sa, ptr.To(tokenValidity))
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error creating serviceaccount token: %w", err)
	}

	kubeconfig, err := clusteraccess.CreateTokenKubeconfig(r.providerName, restCfg.Host, restCfg.CAData, token.Token)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error creating token kubeconfig: %w", err)
	}

	if err := r.writeAccessSecret(ctx, ar, map[string][]byte{
		clustersv1alpha1.SecretKeyKubeconfig:          kubeconfig,
		clustersv1alpha1.SecretKeyCreationTimestamp:   []byte(strconv.FormatInt(token.CreationTimestamp.Unix(), 10)),
		clustersv1alpha1.SecretKeyExpirationTimestamp: []byte(strconv.FormatInt(token.ExpirationTimestamp.Unix(), 10)),
	}); err != nil {
		return reconcile.Result{}, err
	}

	if err := cleanupResources(ctx, targetClient, keep, managedResourcesLabels(ar)); err != nil {
		return reconcile.Result{}, err
	}

	renewal := clusteraccess.ComputeTokenRenewalTimeWithRatio(token.CreationTimestamp, token.ExpirationTimestamp, refreshRatio)
	return reconcile.Result{RequeueAfter: time.Until(renewal)}, nil
}

// ensurePermissions creates the requested (Cluster)Roles and bindings and
// binds the serviceaccount to the referenced existing (Cluster)Roles. It
// returns the objects that must survive the cleanup.
func (r *AccessRequestReconciler) ensurePermissions(ctx context.Context, targetClient client.Client, sa *corev1.ServiceAccount, ar *clustersv1alpha1.AccessRequest) ([]client.Object, error) {
	log := logf.FromContext(ctx)
	expectedLabels := pairs.MapToPairs(managedResourcesLabels(ar))
	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.Name, Namespace: sa.Namespace}}
	nameHash := ctrlutils.NameHashSHAKE128Base32(r.environment, r.providerName, ar.Namespace, ar.Name)

	keep := []client.Object{}
	for i, permission := range ar.Spec.Token.Permissions {
		roleName := permission.Name
		if roleName == "" {
			roleName = fmt.Sprintf("openmcp:permission:%s:%d", nameHash, i)
		}
		if permission.Namespace != "" {
			if !permission.DisableAutomaticNamespaceCreation {
				if _, err := clusteraccess.EnsureNamespace(ctx, targetClient, permission.Namespace); err != nil {
					return nil, fmt.Errorf("error ensuring namespace %q for role %q: %w", permission.Namespace, roleName, err)
				}
			}
			log.V(2).Info("Ensuring Role and RoleBinding", "roleName", roleName, "namespace", permission.Namespace)
			rb, role, err := clusteraccess.EnsureRoleAndBinding(ctx, targetClient, roleName, permission.Namespace, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				return nil, fmt.Errorf("error ensuring role and binding %q/%q: %w", permission.Namespace, roleName, err)
			}
			keep = append(keep, role, rb)
		} else {
			log.V(2).Info("Ensuring ClusterRole and ClusterRoleBinding", "roleName", roleName)
			crb, cr, err := clusteraccess.EnsureClusterRoleAndBinding(ctx, targetClient, roleName, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				return nil, fmt.Errorf("error ensuring cluster role and binding %q: %w", roleName, err)
			}
			keep = append(keep, cr, crb)
		}
	}

	for i, roleRef := range ar.Spec.Token.RoleRefs {
		bindingName := fmt.Sprintf("openmcp:roleref:%s:%d", nameHash, i)
		if roleRef.Kind == "Role" {
			rb, err := clusteraccess.EnsureRoleBinding(ctx, targetClient, bindingName, roleRef.Namespace, roleRef.Name, subjects, expectedLabels...)
			if err != nil {
				return nil, fmt.Errorf("error ensuring role binding %q/%q: %w", roleRef.Namespace, bindingName, err)
			}
			keep = append(keep, rb)
		} else {
			crb, err := clusteraccess.EnsureClusterRoleBinding(ctx, targetClient, bindingName, roleRef.Name, subjects, expectedLabels...)
			if err != nil {
				return nil, fmt.Errorf("error ensuring cluster role binding %q: %w", bindingName, err)
			}
			keep = append(keep, crb)
		}
	}
	return keep, nil
}

// writeAccessSecret stores the access data in the AccessRequest's secret and
// marks the request granted.
func (r *AccessRequestReconciler) writeAccessSecret(ctx context.Context, ar *clustersv1alpha1.AccessRequest, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName(ar),
			Namespace: ar.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.platformCluster.Client(), secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return controllerutil.SetControllerReference(ar, secret, r.platformCluster.Scheme())
	})
	if err != nil {
		return fmt.Errorf("error creating/updating access secret %q/%q: %w", ar.Namespace, secret.Name, err)
	}

	ar.Status.SecretRef = &commonapi.LocalObjectReference{
		Name: secret.Name,
	}
	ar.Status.Phase = clustersv1alpha1.REQUEST_GRANTED
	return nil
}

// targetClient returns a client for the k3d cluster with the given name.
func (r *AccessRequestReconciler) targetClient(ctx context.Context, name string) (client.Client, *rest.Config, error) {
	kubeconfig, err := r.provider.Kubeconfig(ctx, name, true)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting kubeconfig for k3d cluster %q: %w", name, err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing kubeconfig for k3d cluster %q: %w", name, err)
	}
	cl, err := client.New(restCfg, client.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("error creating client for k3d cluster %q: %w", name, err)
	}
	return cl, restCfg, nil
}

// tokenStillValid reports whether the granted token needs no renewal yet and
// how long to wait until the next check.
func tokenStillValid(ctx context.Context, platformClient client.Client, ar *clustersv1alpha1.AccessRequest) (time.Duration, bool) {
	if ar.Status.Phase != clustersv1alpha1.REQUEST_GRANTED || ar.Status.SecretRef == nil {
		return 0, false
	}
	secret := &corev1.Secret{}
	if err := platformClient.Get(ctx, client.ObjectKey{Name: ar.Status.SecretRef.Name, Namespace: ar.Namespace}, secret); err != nil {
		return 0, false
	}
	creation, err := strconv.ParseInt(string(secret.Data[clustersv1alpha1.SecretKeyCreationTimestamp]), 10, 64)
	if err != nil {
		return 0, false
	}
	expiration, err := strconv.ParseInt(string(secret.Data[clustersv1alpha1.SecretKeyExpirationTimestamp]), 10, 64)
	if err != nil {
		return 0, false
	}
	renewal := clusteraccess.ComputeTokenRenewalTimeWithRatio(time.Unix(creation, 0), time.Unix(expiration, 0), refreshRatio)
	if time.Now().After(renewal) {
		return 0, false
	}
	return time.Until(renewal), true
}

func managedResourcesLabels(ar *clustersv1alpha1.AccessRequest) map[string]string {
	return map[string]string{
		managedByNameLabel:      ar.Name,
		managedByNamespaceLabel: ar.Namespace,
	}
}

func secretName(ar *clustersv1alpha1.AccessRequest) string {
	suffix := ".kubeconfig"
	return ctrlutils.ShortenToXCharactersUnsafe(ar.Name, ctrlutils.K8sMaxNameLength-len(suffix)) + suffix
}

// cleanupGVKs are the kinds cleaned up in the target cluster.
var cleanupGVKs = []schema.GroupVersionKind{
	rbacv1.SchemeGroupVersion.WithKind("RoleBindingList"),
	rbacv1.SchemeGroupVersion.WithKind("RoleList"),
	rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBindingList"),
	rbacv1.SchemeGroupVersion.WithKind("ClusterRoleList"),
	corev1.SchemeGroupVersion.WithKind("ServiceAccountList"),
}

// cleanupResources deletes all labeled resources in the target cluster that
// are not part of keep.
func cleanupResources(ctx context.Context, targetClient client.Client, keep []client.Object, labels map[string]string) error {
	log := logf.FromContext(ctx)
	for _, gvk := range cleanupGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := targetClient.List(ctx, list, client.MatchingLabels(labels)); err != nil {
			return fmt.Errorf("error listing %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			keepThis := slices.ContainsFunc(keep, func(obj client.Object) bool {
				return obj.GetObjectKind().GroupVersionKind() == item.GroupVersionKind() &&
					obj.GetName() == item.GetName() && obj.GetNamespace() == item.GetNamespace()
			})
			if keepThis {
				continue
			}
			log.Info("Deleting stale object", "kind", item.GetKind(), "resourceName", item.GetName(), "resourceNamespace", item.GetNamespace())
			if err := targetClient.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("error deleting %s %q/%q: %w", item.GetKind(), item.GetNamespace(), item.GetName(), err)
			}
		}
	}
	return nil
}
