//go:generate opencontrolplane-gen
package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	// opencontrolplane-gen:replace github.com/openmcp-project/cluster-provider-template=MODULE
	"github.com/openmcp-project/cluster-provider-template/api/v1alpha1"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/clusterprovider"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	"github.com/openmcp-project/openmcp-operator/api/common"
	openmcpconditions "github.com/openmcp-project/openmcp-testing/pkg/conditions"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
)

const openmcpSystem = "openmcp-system"

func TestClusterProvider(t *testing.T) {
	basicClusterProviderTest := features.New("provider test").
		WithSetup("create provider config", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			v1alpha1.AddToScheme(c.Client().Resources().GetScheme())
			clustersv1alpha1.AddToScheme(c.Client().Resources().GetScheme())
			config := &v1alpha1.ProviderConfig{}
			// opencontrolplane-gen:replace configname=PROVIDER_NAME
			config.SetName("configname")
			if err := c.Client().Resources().Create(ctx, config); err != nil {
				t.Errorf("failed to create ProviderConfig: %v", err)
			}
			return ctx
		}).
		Assess("verify cluster profiles have been created", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			clusterProfile := clustersv1alpha1.ClusterProfile{}
			// TODO replace with a cluster profile created by your cluster provided
			clusterProfile.Name = "kind"
			list := &clustersv1alpha1.ClusterProfileList{
				Items: []clustersv1alpha1.ClusterProfile{
					clusterProfile,
				},
			}
			if err := wait.For(conditions.New(c.Client().Resources()).ResourcesFound(list)); err != nil {
				t.Errorf("cluster profile not found: %v", err)
			}
			return ctx
		}).
		Assess("verify control plane cluster request result in working cluster", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			clusterRequest := &clustersv1alpha1.ClusterRequest{}
			clusterRequest.SetName("test-cluster")
			clusterRequest.SetNamespace(openmcpSystem)
			clusterRequest.Spec.Purpose = "test"
			if err := c.Client().Resources().Create(ctx, clusterRequest); err != nil {
				t.Errorf("failed to create cluster request: %v", err)
				return ctx
			}
			cluster := &clustersv1alpha1.Cluster{}
			cluster.SetName("test")
			cluster.SetNamespace(openmcpSystem)
			if err := wait.For(openmcpconditions.Match(cluster, c, "Ready", corev1.ConditionTrue)); err != nil {
				t.Errorf("cluster is not ready")
			}
			return ctx
		}).
		Assess("verify access request result in kubeconfig for created control plane", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			accessRequest := &clustersv1alpha1.AccessRequest{}
			accessRequest.SetName("test")
			accessRequest.SetNamespace(openmcpSystem)
			accessRequest.Spec.ClusterRef = &common.ObjectReference{
				Name:      "test",
				Namespace: openmcpSystem,
			}
			accessRequest.Spec.Token = &clustersv1alpha1.TokenConfig{
				RoleRefs: []common.RoleRef{
					{
						Name: "cluster-admin",
						Kind: "ClusterRole",
					},
				},
			}
			if err := c.Client().Resources().Create(ctx, accessRequest); err != nil {
				t.Errorf("failed to created access request: %v", err)
				return ctx
			}
			if err := wait.For(openmcpconditions.Match(accessRequest, c, "Ready", corev1.ConditionTrue)); err != nil {
				t.Errorf("access request is not ready")
			}
			return ctx
		}).
		Assess("create dummy resource on created cluster", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			var accessRequest clustersv1alpha1.AccessRequest
			if err := c.Client().Resources().Get(ctx, "test", openmcpSystem, &accessRequest); err != nil {
				t.Errorf("failed to retreive access request: %v", err)
				return ctx
			}
			var kubeconfigSecret corev1.Secret
			if err := c.Client().Resources().Get(ctx, accessRequest.Status.SecretRef.Name, openmcpSystem, &kubeconfigSecret); err != nil {
				t.Errorf("failed to retreive kubeconfig secret: %v", err)
				return ctx
			}
			kubeconfig, ok := kubeconfigSecret.Data[clustersv1alpha1.SecretKeyKubeconfig]
			if !ok {
				t.Error("failed to retrieve kubeconfig from secret")
				return ctx
			}
			cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
			if err != nil {
				t.Errorf("failed to create rest config based on kubeconfig: %v", err)
				return ctx
			}
			// TODO the host override is a cluster provider kind specific adjustment to reach the cluster
			if localhost, ok := accessRequest.GetAnnotations()[clusterprovider.LocalAccessAnnotation]; ok {
				cfg.Host = localhost
			}
			cl, err := client.New(cfg, client.Options{})
			if err != nil {
				t.Errorf("failed to create cluster client: %v", err)
				return ctx
			}
			configMap := &corev1.ConfigMap{}
			configMap.SetName("test")
			configMap.SetNamespace(corev1.NamespaceDefault)
			configMap.Data = map[string]string{
				"foo": "bar",
			}
			if err := cl.Create(ctx, configMap); err != nil {
				t.Errorf("failed to create dummy config map on new cluster")
			}
			return ctx
		},
		).
		Assess("verify cluster is successfully deleted", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if err := providers.DeleteCluster(ctx, c, types.NamespacedName{Namespace: openmcpSystem, Name: "test"}); err != nil {
				t.Errorf("delete cluster failed: %v", err)
			}
			return ctx
		})
	testenv.Test(t, basicClusterProviderTest.Feature())
}
