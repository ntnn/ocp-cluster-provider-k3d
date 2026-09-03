package cluster

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types on the Cluster resource.
const (
	// ConditionK3dReady reports whether the backing k3d cluster exists and
	// came up.
	ConditionK3dReady = "K3dReady"
	// ConditionReady is the aggregate readiness of the Cluster.
	ConditionReady = "Ready"
)

func (r *reconciler) setConditionReady(status bool, reason, message string) {
	r.setCondition(ConditionReady, status, reason, message)
}

func (r *reconciler) setConditionK3dReady(status bool, reason, message string) {
	r.setCondition(ConditionK3dReady, status, reason, message)
	// A failed prerequisite immediately falsifies the aggregate.
	if !status {
		r.setConditionReady(false, "K3dNotReady", "k3d cluster is not ready")
	}
}

func (r *reconciler) setCondition(conditionType string, status bool, reason, message string) {
	metaStatus := metav1.ConditionTrue
	if !status {
		metaStatus = metav1.ConditionFalse
	}
	meta.SetStatusCondition(
		&r.cluster.Status.Conditions,
		metav1.Condition{
			Type:               conditionType,
			ObservedGeneration: r.cluster.Generation,
			Status:             metaStatus,
			Reason:             reason,
			Message:            message,
		},
	)
}
