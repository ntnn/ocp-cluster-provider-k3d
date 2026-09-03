package accessrequest

import (
	"context"
	"fmt"

	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/cluster-provider-k3d/pkg/k3d"
)

type AccessRequestReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
	environment     string
	provider        k3d.Provider
}

func NewAccessRequestReconciler(platformCluster *clusters.Cluster, providerName, environment string, provider k3d.Provider) *AccessRequestReconciler {
	return &AccessRequestReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
		environment:     environment,
		provider:        provider,
	}
}

func (r *AccessRequestReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)
	// 1. get obj
	obj := &clustersv1alpha1.AccessRequest{}
	if err := r.platformCluster.Client().Get(ctx, req.NamespacedName, obj); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if obj.GetAnnotations() != nil {
		op, ok := obj.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return reconcile.Result{}, nil
			case openmcpconst.OperationAnnotationValueReconcile:
				if err := ctrlutils.EnsureAnnotation(ctx, r.platformCluster.Client(), obj, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return reconcile.Result{}, fmt.Errorf("error removing operation annotation: %w", err)
				}
				log.Info("Manual reconciliation triggered with operation annotation")
			}
		}
	}
	// 2. reconcile and report status
	if !libutils.IsClusterProviderResponsibleForAccessRequest(obj, r.providerName) {
		log.Info("Not responsible for this AccessRequest, skipping")
		return reconcile.Result{}, nil
	}
	if obj.Spec.ClusterRef == nil {
		return reconcile.Result{}, r.updateStatus(ctx, obj, obj.DeepCopy(), fmt.Errorf("AccessRequest %q/%q has no cluster reference", obj.Namespace, obj.Name))
	}

	cluster := &clustersv1alpha1.Cluster{}
	clusterRef := types.NamespacedName{Name: obj.Spec.ClusterRef.Name, Namespace: obj.Spec.ClusterRef.Namespace}
	if err := r.platformCluster.Client().Get(ctx, clusterRef, cluster); err != nil {
		return reconcile.Result{}, r.updateStatus(ctx, obj, obj.DeepCopy(), fmt.Errorf("error getting Cluster %q: %w", clusterRef, err))
	}

	old := obj.DeepCopy()
	if !obj.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, r.updateStatus(ctx, obj, old, r.handleDelete(ctx, obj, cluster))
	}
	res, err := r.handleCreateOrUpdate(ctx, obj, cluster)
	return res, r.updateStatus(ctx, obj, old, err)
}

// updateStatus reports the reconcile outcome on the AccessRequest and returns
// the reconcile error.
func (r *AccessRequestReconciler) updateStatus(ctx context.Context, obj, old *clustersv1alpha1.AccessRequest, reconcileError error) error {
	status := metav1.ConditionTrue
	reason := "ReconcileSuccess"
	message := "AccessRequest is ready"
	if reconcileError != nil {
		status = metav1.ConditionFalse
		reason = "ReconcileError"
		message = reconcileError.Error()
	}
	meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: obj.Generation,
	})
	obj.Status.ObservedGeneration = obj.Generation
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		if err := r.platformCluster.Client().Status().Patch(ctx, obj, client.MergeFrom(old)); err != nil {
			return fmt.Errorf("error patching AccessRequest status: %w", err)
		}
	}
	return reconcileError
}

func (r *AccessRequestReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clustersv1alpha1.AccessRequest{}, builder.WithPredicates(
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				ar, ok := obj.(*clustersv1alpha1.AccessRequest)
				if !ok {
					return false
				}
				return libutils.IsClusterProviderResponsibleForAccessRequest(ar, r.providerName)
			}),
		)).
		Owns(&corev1.Secret{}).
		Complete(r)
}
