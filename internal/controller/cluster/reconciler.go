package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"

	"github.com/openmcp-project/cluster-provider-k3d/api/v1alpha1"
)

// foreignFinalizerRequeue is the poll interval while waiting for other
// controllers to release their finalizers on a deleted Cluster.
const foreignFinalizerRequeue = 10 * time.Second

type reconciler struct {
	opts Options
	log  logr.Logger

	old     *clustersv1alpha1.Cluster
	cluster *clustersv1alpha1.Cluster
}

func (r *reconciler) reconcile(ctx context.Context) (reconcile.Result, error) {
	responsible, err := r.responsible(ctx)
	if err != nil || !responsible {
		return reconcile.Result{}, err
	}

	result, err := r.run(ctx)
	return result, errors.Join(err, r.commitStatus(ctx))
}

// responsible reports whether the Cluster's profile references this provider.
func (r *reconciler) responsible(ctx context.Context) (bool, error) {
	profile := &clustersv1alpha1.ClusterProfile{}
	if err := r.opts.PlatformCluster.Client().Get(ctx, client.ObjectKey{Name: r.cluster.Spec.Profile}, profile); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.V(2).Info("skipping Cluster, ClusterProfile not found", "profile", r.cluster.Spec.Profile)
			return false, nil
		}
		return false, fmt.Errorf("getting ClusterProfile %q: %w", r.cluster.Spec.Profile, err)
	}
	if profile.Spec.ProviderRef.Name != r.opts.ProviderName {
		r.log.V(2).Info("skipping Cluster, not responsible", "profile", r.cluster.Spec.Profile, "provider", profile.Spec.ProviderRef.Name)
		return false, nil
	}
	return true, nil
}

func (r *reconciler) run(ctx context.Context) (reconcile.Result, error) {
	if !r.cluster.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx)
	}
	if cont, err := r.ensureFinalizer(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}
	if err := r.ensureK3dCluster(ctx); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, r.publishAccess(ctx)
}

func (r *reconciler) commitStatus(ctx context.Context) error {
	r.cluster.Status.ObservedGeneration = r.cluster.Generation
	if equality.Semantic.DeepEqual(r.old.Status, r.cluster.Status) {
		return nil
	}
	if err := r.opts.PlatformCluster.Client().Status().Patch(ctx, r.cluster, client.MergeFrom(r.old)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patching Cluster status: %w", err)
	}
	return nil
}

func (r *reconciler) handleDeletion(ctx context.Context) (reconcile.Result, error) {
	r.cluster.Status.Phase = commonapi.StatusPhaseTerminating

	if !controllerutil.ContainsFinalizer(r.cluster, Finalizer) {
		return reconcile.Result{}, nil
	}
	// Foreign finalizers first: other controllers still need the k3d cluster.
	if foreign := foreignFinalizers(r.cluster); len(foreign) > 0 {
		r.log.V(2).Info("waiting for foreign finalizers", "finalizers", foreign)
		return reconcile.Result{RequeueAfter: foreignFinalizerRequeue}, nil
	}

	name := K3dName(r.cluster)
	exists, err := r.opts.Provider.ClusterExists(ctx, name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if exists {
		if err := r.opts.Provider.DeleteCluster(ctx, name); err != nil {
			return reconcile.Result{}, err
		}
		r.log.Info("Deleted k3d cluster", "name", name)
	}

	controllerutil.RemoveFinalizer(r.cluster, Finalizer)
	if err := r.opts.PlatformCluster.Client().Update(ctx, r.cluster); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return reconcile.Result{}, nil
}

func (r *reconciler) ensureFinalizer(ctx context.Context) (bool, error) {
	if !controllerutil.AddFinalizer(r.cluster, Finalizer) {
		return true, nil
	}
	if err := r.opts.PlatformCluster.Client().Update(ctx, r.cluster); err != nil {
		return false, fmt.Errorf("adding finalizer: %w", err)
	}
	return false, nil
}

func (r *reconciler) ensureK3dCluster(ctx context.Context) error {
	r.cluster.Status.Phase = commonapi.StatusPhaseProgressing

	name := K3dName(r.cluster)
	exists, err := r.opts.Provider.ClusterExists(ctx, name)
	if err != nil {
		r.setConditionK3dReady(false, "ClusterLookupFailed", err.Error())
		return err
	}
	if !exists {
		// CreateCluster blocks until the cluster is ready.
		if err := r.opts.Provider.CreateCluster(ctx, name); err != nil {
			r.setConditionK3dReady(false, "ClusterCreationFailed", err.Error())
			return err
		}
		r.log.Info("Created k3d cluster", "name", name)
	}
	r.setConditionK3dReady(true, "ClusterExists", "")
	return nil
}

// publishAccess writes the provider status and the external and internal API
// server endpoints, then marks the Cluster ready.
func (r *reconciler) publishAccess(ctx context.Context) error {
	name := K3dName(r.cluster)

	err := r.cluster.Status.SetProviderStatus(v1alpha1.ClusterStatus{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "ClusterStatus",
		},
		K3dClusterName: name,
	})
	if err != nil {
		return fmt.Errorf("setting provider status: %w", err)
	}

	externalCfg, err := r.restConfig(ctx, name, false)
	if err != nil {
		return err
	}
	internalCfg, err := r.restConfig(ctx, name, true)
	if err != nil {
		return err
	}
	r.cluster.Status.Endpoints = clustersv1alpha1.Endpoints{}
	r.cluster.Status.Endpoints.Set(clustersv1alpha1.APISERVER_ENDPOINT_EXTERNAL, externalCfg.Host)
	r.cluster.Status.Endpoints.Set(clustersv1alpha1.APISERVER_ENDPOINT_INTERNAL, internalCfg.Host)

	if err := r.ensureDisplayMetadata(ctx, internalCfg, name); err != nil {
		return err
	}

	r.cluster.Status.Phase = commonapi.StatusPhaseReady
	r.setConditionReady(true, "ClusterReady", "")
	return nil
}

// ensureDisplayMetadata maintains the labels and annotation that back the
// kubectl printcolumns (VERSION, PROVIDER, INFO) on the Cluster resource.
// Patched via MergeFrom so only these keys are sent, like the gardener
// provider does.
func (r *reconciler) ensureDisplayMetadata(ctx context.Context, cfg *rest.Config, name string) error {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating discovery client for k3d cluster %q: %w", name, err)
	}
	serverVersion, err := discoveryClient.ServerVersion()
	if err != nil {
		return fmt.Errorf("discovering server version of k3d cluster %q: %w", name, err)
	}

	old := r.cluster.DeepCopy()
	metav1.SetMetaDataLabel(&r.cluster.ObjectMeta, clustersv1alpha1.ProviderLabel, r.opts.ProviderName)
	// '+' (as in v1.32.5+k3s1) is not a valid label value character.
	metav1.SetMetaDataLabel(&r.cluster.ObjectMeta, clustersv1alpha1.K8sVersionLabel, strings.ReplaceAll(serverVersion.GitVersion, "+", "_"))
	metav1.SetMetaDataAnnotation(&r.cluster.ObjectMeta, clustersv1alpha1.ProviderInfoAnnotation, name)
	if equality.Semantic.DeepEqual(old.ObjectMeta, r.cluster.ObjectMeta) {
		return nil
	}
	if err := r.opts.PlatformCluster.Client().Patch(ctx, r.cluster, client.MergeFrom(old)); err != nil {
		return fmt.Errorf("patching Cluster display metadata: %w", err)
	}
	return nil
}

func (r *reconciler) restConfig(ctx context.Context, name string, internal bool) (*rest.Config, error) {
	kubeconfig, err := r.opts.Provider.Kubeconfig(ctx, name, internal)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig for k3d cluster %q: %w", name, err)
	}
	return restConfig, nil
}

func foreignFinalizers(cluster *clustersv1alpha1.Cluster) []string {
	foreign := make([]string, 0, len(cluster.Finalizers))
	for _, fin := range cluster.Finalizers {
		if fin != Finalizer {
			foreign = append(foreign, fin)
		}
	}
	return foreign
}
