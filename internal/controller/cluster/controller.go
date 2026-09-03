package cluster

import (
	"context"
	"fmt"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ClusterReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
}

func NewClusterReconciler(platformCluster *clusters.Cluster, providerName string) *ClusterReconciler {
	return &ClusterReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
	}
}

func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)
	// 1. get obj
	obj := &clustersv1alpha1.Cluster{}
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
	// 2. TODO: check if provider is responsible for requested cluster profile (obj.Spec.Profile)
	// 3. TODO: reconcile obj and report status
	if len(obj.Status.Conditions) == 0 {
		meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "ReconcileSuccess",
			Message: "Cluster is ready",
		})
		obj.Status.ObservedGeneration = obj.GetGeneration()
		obj.Status.Phase = "Ready"
	}
	if err := r.platformCluster.Client().Status().Update(ctx, obj); err != nil {
		log.Error(err, "Failed to update Cluster status")
		return ctrl.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ClusterReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clustersv1alpha1.Cluster{}).
		Watches(&clustersv1alpha1.ClusterProfile{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			if obj == nil {
				return nil
			}
			// reconcile all clusters that reference this profile
			clusters := &clustersv1alpha1.ClusterList{}
			if err := r.platformCluster.Client().List(ctx, clusters, client.MatchingFields{
				"spec.profile": obj.GetName(),
			}); err != nil {
				logf.FromContext(ctx).Error(err, "failed to list cluster profiles")
				return nil
			}
			reqs := make([]reconcile.Request, len(clusters.Items))
			for i, cluster := range clusters.Items {
				reqs[i] = reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&cluster),
				}
			}
			return reqs
		})).
		Complete(r)
}
