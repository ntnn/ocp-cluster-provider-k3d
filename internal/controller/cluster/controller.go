package cluster

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	"github.com/openmcp-project/cluster-provider-k3d/pkg/k3d"
)

// ControllerName is the name of the Cluster controller.
const ControllerName = "cluster"

// Finalizer is the finalizer this provider sets on Cluster resources.
var Finalizer = clustersv1alpha1.GroupVersion.Group + "/finalizer"

// profileIndex is the field index used to map ClusterProfiles to Clusters.
const profileIndex = "spec.profile"

// Options configures the Cluster controller.
type Options struct {
	PlatformCluster *clusters.Cluster
	// ProviderName identifies this provider deployment; Clusters whose
	// ClusterProfile references it are reconciled.
	ProviderName string
	// Provider manages the backing k3d clusters.
	Provider k3d.Provider
}

func (o *Options) validate() error {
	if o.PlatformCluster == nil {
		return errors.New("PlatformCluster is required")
	}
	if o.ProviderName == "" {
		return errors.New("ProviderName is required")
	}
	if o.Provider == nil {
		return errors.New("provider is required")
	}
	return nil
}

// ClusterReconciler reconciles Cluster resources.
type ClusterReconciler struct {
	opts Options
}

// NewClusterReconciler returns a Cluster reconciler for the given options.
func NewClusterReconciler(opts Options) (*ClusterReconciler, error) {
	if err := opts.validate(); err != nil {
		return nil, fmt.Errorf("invalid cluster controller options: %w", err)
	}
	return &ClusterReconciler{opts: opts}, nil
}

// Reconcile loads the Cluster, honors the operation annotation and runs a single reconcile pass.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	obj := &clustersv1alpha1.Cluster{}
	if err := r.opts.PlatformCluster.Client().Get(ctx, req.NamespacedName, obj); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err) //nolint:wrapcheck // nil or NotFound only
	}

	switch obj.GetAnnotations()[openmcpconst.OperationAnnotation] {
	case openmcpconst.OperationAnnotationValueIgnore:
		log.Info("Ignoring resource due to ignore operation annotation")
		return reconcile.Result{}, nil
	case openmcpconst.OperationAnnotationValueReconcile:
		if err := ctrlutils.EnsureAnnotation(ctx, r.opts.PlatformCluster.Client(), obj, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
			return reconcile.Result{}, fmt.Errorf("error removing operation annotation: %w", err)
		}
		log.Info("Manual reconciliation triggered with operation annotation")
	}

	rec := &reconciler{
		opts:    r.opts,
		log:     log,
		old:     obj,
		cluster: obj.DeepCopy(),
	}
	return rec.reconcile(ctx)
}

// SetupWithManager registers the controller, the profile field index and a watch that re-triggers all Clusters referencing a changed ClusterProfile.
func (r *ClusterReconciler) SetupWithManager(mgr manager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &clustersv1alpha1.Cluster{}, profileIndex, func(obj client.Object) []string {
		cluster, ok := obj.(*clustersv1alpha1.Cluster)
		if !ok {
			return nil
		}
		return []string{cluster.Spec.Profile}
	}); err != nil {
		return fmt.Errorf("registering %s index: %w", profileIndex, err)
	}

	err := ctrl.NewControllerManagedBy(mgr).
		Named(ControllerName).
		For(&clustersv1alpha1.Cluster{}).
		Watches(&clustersv1alpha1.ClusterProfile{}, handler.EnqueueRequestsFromMapFunc(r.clustersForProfile)).
		Complete(r)
	if err != nil {
		return fmt.Errorf("building cluster controller: %w", err)
	}
	return nil
}

func (r *ClusterReconciler) clustersForProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterList := &clustersv1alpha1.ClusterList{}
	if err := r.opts.PlatformCluster.Client().List(ctx, clusterList, client.MatchingFieldsSelector{
		Selector: fields.OneTermEqualSelector(profileIndex, obj.GetName()),
	}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to list Clusters for ClusterProfile")
		return nil
	}
	reqs := make([]reconcile.Request, len(clusterList.Items))
	for i := range clusterList.Items {
		reqs[i] = reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&clusterList.Items[i]),
		}
	}
	return reqs
}
