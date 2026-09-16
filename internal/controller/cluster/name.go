package cluster

import (
	"fmt"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"

	"github.com/openmcp-project/cluster-provider-k3d/api/v1alpha1"
)

// AnnotationName overrides the name of the backing k3d cluster.
var AnnotationName = v1alpha1.GroupVersion.Group + "/name"

// ocpctl hardcodes the kind annotation, so for cluster-provider-k3d to
// work with ocpctl's local env it needs to honour the kind annotation
// as well.
const AnnotationNameKind = "kind.clusters.openmcp.cloud/name"

// K3dName returns the name of the k3d cluster backing the given Cluster.
// AnnotationName, then AnnotationNameKind, override the derived name.
func K3dName(cluster *clustersv1alpha1.Cluster) string {
	if name, ok := cluster.Annotations[AnnotationName]; ok {
		return name
	}
	if name, ok := cluster.Annotations[AnnotationNameKind]; ok {
		return name
	}
	return fmt.Sprintf("%s-%s", cluster.Name, string(cluster.UID)[:8])
}
