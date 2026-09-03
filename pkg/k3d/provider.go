package k3d

import (
	"context"
	"fmt"
	"slices"
	"time"

	k3dclient "github.com/k3d-io/k3d/v5/pkg/client"
	k3dconfig "github.com/k3d-io/k3d/v5/pkg/config"
	k3dv1alpha5 "github.com/k3d-io/k3d/v5/pkg/config/v1alpha5"
	k3druntimes "github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3dtypes "github.com/k3d-io/k3d/v5/pkg/types"
	k3dversion "github.com/k3d-io/k3d/v5/version"
	"github.com/spf13/viper"
	"k8s.io/client-go/tools/clientcmd"
)

// Options configures the Provider.
type Options struct {
	// Runtime is the container runtime k3d operates on.
	Runtime k3druntimes.Runtime
	// ConfigFile is an optional path to a k3d SimpleConfig YAML applied
	// to every created cluster.
	ConfigFile string
	// Timeout bounds the wait for a created cluster to become ready.
	Timeout time.Duration
}

func (o *Options) validate() {
	if o.Runtime == nil {
		o.Runtime = k3druntimes.SelectedRuntime
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
}

// Provider defines the interface for managing Kubernetes clusters using k3d.
// It provides methods to create, delete, check existence of clusters, and retrieve kubeconfig.
type Provider interface {
	// CreateCluster creates a new Kubernetes cluster with the given name.
	CreateCluster(ctx context.Context, name string) error

	// DeleteCluster deletes the Kubernetes cluster with the given name.
	DeleteCluster(ctx context.Context, name string) error

	// ClusterExists checks if a Kubernetes cluster with the given name exists.
	ClusterExists(ctx context.Context, name string) (bool, error)

	// KubeConfig retrieves the kubeconfig for the specified cluster name. The bool localhosts indicates whether the function returns a kubeconfig with the cluster's serverlb node name or the host address.
	Kubeconfig(ctx context.Context, name string, internal bool) (string, error)
}

// k3dProvider manages k3d clusters through the k3d client API.
type k3dProvider struct {
	opts Options
}

var _ Provider = &k3dProvider{}

// New returns a Provider backed by the k3d client API.
func New(opts Options) Provider {
	opts.validate()
	return &k3dProvider{opts: opts}
}

// CreateCluster implements Provider.
func (provider *k3dProvider) CreateCluster(ctx context.Context, name string) error {
	simple, err := provider.simpleConfig(name)
	if err != nil {
		return err
	}

	clusterConfig, err := k3dconfig.TransformSimpleToClusterConfig(
		ctx,
		provider.opts.Runtime,
		simple,
		provider.opts.ConfigFile,
	)
	if err != nil {
		return fmt.Errorf("transforming k3d config for cluster %q: %w", name, err)
	}

	clusterConfig, err = k3dconfig.ProcessClusterConfig(*clusterConfig)
	if err != nil {
		return fmt.Errorf("processing k3d config for cluster %q: %w", name, err)
	}

	if err := k3dconfig.ValidateClusterConfig(ctx, provider.opts.Runtime, *clusterConfig); err != nil {
		return fmt.Errorf("validating k3d config for cluster %q: %w", name, err)
	}

	if err := k3dclient.ClusterRun(ctx, provider.opts.Runtime, clusterConfig); err != nil {
		return fmt.Errorf("creating k3d cluster %q: %w", name, err)
	}

	return nil
}

// DeleteCluster implements Provider.
func (provider *k3dProvider) DeleteCluster(ctx context.Context, name string) error {
	cluster, err := k3dclient.ClusterGet(ctx, provider.opts.Runtime, &k3dtypes.Cluster{Name: name})
	if err != nil {
		return fmt.Errorf("getting k3d cluster %q: %w", name, err)
	}

	if err := k3dclient.ClusterDelete(ctx, provider.opts.Runtime, cluster, k3dtypes.ClusterDeleteOpts{}); err != nil {
		return fmt.Errorf("deleting k3d cluster %q: %w", name, err)
	}

	return nil
}

// ClusterExists implements Provider.
func (provider *k3dProvider) ClusterExists(ctx context.Context, name string) (bool, error) {
	clusters, err := k3dclient.ClusterList(ctx, provider.opts.Runtime)
	if err != nil {
		return false, fmt.Errorf("listing k3d clusters: %w", err)
	}

	return slices.ContainsFunc(
		clusters,
		func(c *k3dtypes.Cluster) bool {
			return c.Name == name
		},
	), nil
}

// Kubeconfig implements Provider.
func (provider *k3dProvider) Kubeconfig(ctx context.Context, name string, internal bool) (string, error) {
	cluster, err := k3dclient.ClusterGet(ctx, provider.opts.Runtime, &k3dtypes.Cluster{Name: name})
	if err != nil {
		return "", fmt.Errorf("getting k3d cluster %q: %w", name, err)
	}

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, provider.opts.Runtime, cluster)
	if err != nil {
		return "", fmt.Errorf("getting kubeconfig for k3d cluster %q: %w", name, err)
	}

	if internal {
		server, err := internalServerURL(cluster)
		if err != nil {
			return "", err
		}

		for _, c := range kubeconfig.Clusters {
			c.Server = server
		}
	}

	raw, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		return "", fmt.Errorf("serializing kubeconfig for k3d cluster %q: %w", name, err)
	}

	return string(raw), nil
}

// internalServerURL returns the API server URL reachable from within the k3d
// docker network, preferring the serverlb node over a plain server node.
func internalServerURL(cluster *k3dtypes.Cluster) (string, error) {
	var server *k3dtypes.Node
	for _, node := range cluster.Nodes {
		if node.Role == k3dtypes.LoadBalancerRole {
			server = node
			break
		}
		if node.Role == k3dtypes.ServerRole && server == nil {
			server = node
		}
	}

	if server == nil {
		return "", fmt.Errorf("k3d cluster %q has no server node", cluster.Name)
	}

	return fmt.Sprintf("https://%s:%s", server.Name, k3dtypes.DefaultAPIPort), nil
}

func (provider *k3dProvider) simpleConfig(name string) (k3dv1alpha5.SimpleConfig, error) {
	v := viper.New()
	if provider.opts.ConfigFile != "" {
		v.SetConfigFile(provider.opts.ConfigFile)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err != nil {
			return k3dv1alpha5.SimpleConfig{}, fmt.Errorf("reading k3d config file %q: %w", provider.opts.ConfigFile, err)
		}
	}

	simple, err := k3dconfig.SimpleConfigFromViper(v)
	if err != nil {
		return k3dv1alpha5.SimpleConfig{}, fmt.Errorf("parsing k3d config file %q: %w", provider.opts.ConfigFile, err)
	}

	simple.Name = name
	if simple.Servers == 0 {
		simple.Servers = 1
	}
	if simple.Image == "" {
		simple.Image = fmt.Sprintf("%s:%s", k3dtypes.DefaultK3sImageRepo, k3dversion.K3sVersion)
	}
	simple.Options.K3dOptions.Wait = true
	simple.Options.K3dOptions.Timeout = provider.opts.Timeout
	simple.Options.KubeconfigOptions.UpdateDefaultKubeconfig = false
	simple.Options.KubeconfigOptions.SwitchCurrentContext = false

	if err := k3dconfig.ProcessSimpleConfig(&simple); err != nil {
		return k3dv1alpha5.SimpleConfig{}, fmt.Errorf("processing k3d simple config for cluster %q: %w", name, err)
	}

	return simple, nil
}
