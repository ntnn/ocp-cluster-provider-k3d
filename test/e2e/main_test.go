//go:generate opencontrolplane-gen
package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/setup"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	initLogging()
	version := mustVersion()
	openmcp := setup.OpenMCPSetup{
		Namespace: "openmcp-system",
		Operator: setup.OpenMCPOperatorSetup{
			Name: "openmcp-operator",
			// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/openmcp-operator
			Image:        "ghcr.io/openmcp-project/images/openmcp-operator:v1.1.0",
			Environment:  "debug",
			PlatformName: "platform",
			// TODO replace with the cluster profile(s) to test your cluster provider
			ExtraClusterPurposeMapping: []providers.ClusterPurposeMapping{
				{
					Purpose: "test",
					Profile: "kind",
					Tenancy: clustersv1alpha1.TENANCY_SHARED,
				},
			},
		},
		ClusterProviders: []providers.ClusterProviderSetup{
			{
				Name: "kind",
				// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/cluster-provider-kind
				Image: "ghcr.io/openmcp-project/images/cluster-provider-kind:v0.4.2",
			},
			{
				// opencontrolplane-gen:replace foo=PROVIDER_NAME
				Name: "foo",
				// opencontrolplane-gen:replace template=PROVIDER_NAME
				Image:              fmt.Sprintf("ghcr.io/openmcp-project/images/cluster-provider-template:%s", version),
				LoadImageToCluster: true,
				// TODO (optional) use DeploymentSpec to override the default deployment spec that is used to deploy your cluster provider
				// DeploymentSpec: &providerv1alpha1.DeploymentSpec{
				// 	...
				// },
			},
		},
	}
	testenv = env.NewWithConfig(envconf.New().WithNamespace(openmcp.Namespace))
	openmcp.Bootstrap(testenv)
	os.Exit(testenv.Run(m))
}

func mustVersion() string {
	cmd := exec.Command("../../hack/common/get-version.sh")
	version, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(version))
}

func initLogging() {
	klog.InitFlags(nil)
	if err := flag.Set("v", "2"); err != nil {
		panic(err)
	}
	flag.Parse()
}
