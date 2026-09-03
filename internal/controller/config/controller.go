//go:generate opencontrolplane-gen
package config

import (
	"context"
	"fmt"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	// opencontrolplane-gen:replace github.com/openmcp-project/cluster-provider-template=MODULE
	"github.com/openmcp-project/cluster-provider-template/api/v1alpha1"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	apiconst "github.com/openmcp-project/openmcp-operator/api/constants"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ProviderConfigReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
}

func NewProviderConfigReconciler(platformCluster *clusters.Cluster, providerName string) *ProviderConfigReconciler {
	return &ProviderConfigReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
	}
}

func (r *ProviderConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)
	// 1. get obj
	obj := &v1alpha1.ProviderConfig{}
	if err := r.platformCluster.Client().Get(ctx, req.NamespacedName, obj); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	// handle operation annotation
	if obj.GetAnnotations() != nil {
		op, ok := obj.GetAnnotations()[apiconst.OperationAnnotation]
		if ok {
			switch op {
			case apiconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource with operation annotation")
				return reconcile.Result{}, nil
			case apiconst.OperationAnnotationValueReconcile:
				if err := ctrlutils.EnsureAnnotation(ctx, r.platformCluster.Client(), obj, apiconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
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
			Message: "ProviderConfig is ready",
		})
		obj.Status.ObservedGeneration = obj.GetGeneration()
		obj.Status.Phase = "Ready"
	}
	if err := r.platformCluster.Client().Status().Update(ctx, obj); err != nil {
		log.Error(err, "Failed to update ProviderConfig status")
		return ctrl.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ProviderConfigReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ProviderConfig{}).
		Complete(r)
}
