package config

import (
	"context"
	"fmt"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/cluster-provider-k3d/api/v1alpha1"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"
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
	// The owned ClusterProfile is cleaned up by garbage collection.
	if !obj.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	// 2. create/update the ClusterProfile pointing the scheduler at this provider
	profile := &clustersv1alpha1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: obj.Name,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.platformCluster.Client(), profile, func() error {
		profile.Spec = clustersv1alpha1.ClusterProfileSpec{
			ProviderRef: commonapi.LocalObjectReference{
				Name: r.providerName,
			},
			ProviderConfigRef: commonapi.LocalObjectReference{
				Name: obj.Name,
			},
			SupportedVersions: []clustersv1alpha1.SupportedK8sVersion{},
		}
		return controllerutil.SetControllerReference(obj, profile, r.platformCluster.Scheme())
	})
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error creating/updating ClusterProfile %q: %w", profile.Name, err)
	}
	meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSuccess",
		Message:            "ProviderConfig is ready",
		ObservedGeneration: obj.GetGeneration(),
	})
	obj.Status.ObservedGeneration = obj.GetGeneration()
	obj.Status.Phase = "Ready"
	if err := r.platformCluster.Client().Status().Update(ctx, obj); err != nil {
		log.Error(err, "Failed to update ProviderConfig status")
		return ctrl.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ProviderConfigReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ProviderConfig{}).
		Owns(&clustersv1alpha1.ClusterProfile{}).
		Complete(r)
}
