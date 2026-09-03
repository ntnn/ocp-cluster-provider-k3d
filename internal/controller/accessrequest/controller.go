package accessrequest

import (
	"context"
	"fmt"

	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
)

type AccessRequestReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
}

func NewAccessRequestReconciler(platformCluster *clusters.Cluster, providerName string) *AccessRequestReconciler {
	return &AccessRequestReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
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
	// 2. TODO: reconcile obj and report status
	if len(obj.Status.Conditions) == 0 {
		meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "ReconcileSuccess",
			Message: "AccessRequest is ready",
		})
		obj.Status.ObservedGeneration = obj.GetGeneration()
		obj.Status.Phase = "Ready"
	}
	if err := r.platformCluster.Client().Status().Update(ctx, obj); err != nil {
		log.Error(err, "Failed to update AccessRequest status")
		return ctrl.Result{}, err
	}
	return reconcile.Result{}, nil
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
		Owns(&corev1.Secret{}). // watch the managed kubeconfig
		Complete(r)
}
