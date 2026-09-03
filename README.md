[![REUSE status](https://api.reuse.software/badge/github.com/openmcp-project/cluster-provider-k3d)](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-k3d)

# cluster-provider-k3d

## About this project

A cluster provider for [OpenMCP](https://github.com/openmcp-project/openmcp-operator) that uses [k3d](https://k3d.io) to provision and manage Kubernetes clusters. This provider enables you to create and manage multiple Kubernetes clusters running as Docker containers, making it ideal for:

- **Local Development**: Quickly spin up multiple clusters for testing multi-cluster scenarios
- **E2E Testing**: Automated testing of multi-cluster applications and operators
- **CI/CD Pipelines**: Lightweight cluster provisioning for testing environments

## 🏗️ Installation

### Local Development

Prerequisites: docker, [k3d](https://k3d.io) v5, kubectl, [task](https://taskfile.dev), go.

Deploy - creates a k3d platform cluster, builds the provider image, deploys the [openmcp-operator](https://github.com/openmcp-project/openmcp-operator), this provider and the [ocpctl](https://github.com/openmcp-project/ocpctl)-default service providers (crossplane, flux, kro, ocm, gateway), waits for the onboarding cluster (the first provisioned k3d cluster):

```shell
./hack/local-dev.sh deploy
```

Tear down, deletes **all** k3d clusters, including provisioned ones:

```shell
./hack/local-dev.sh reset
```

## Requesting a cluster

Create a `Cluster` resource with the `k3d` profile on the platform cluster:

```yaml
apiVersion: clusters.openmcp.cloud/v1alpha1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  kubernetes: {}
  profile: k3d
  purposes:
    - workload
  tenancy: Exclusive
```

```sh
kubectl --kubeconfig ./platform.kubeconfig apply -f ./hack/cluster.yaml
```

The k3d cluster is named `my-cluster-<uid-prefix>`.
The name can be overridden with the `k3d.cluster.open-control-plane.io/name` annotation.

```shell
kubectl wait --for='jsonpath={.status.phase}=Ready' cluster/my-cluster --timeout=120s
kubectl get cluster my-cluster -o jsonpath='{.status}' | jq
```

### Access from within the platform

Create an `AccessRequest` referencing the cluster:

```yaml
apiVersion: clusters.openmcp.cloud/v1alpha1
kind: AccessRequest
metadata:
  name: my-cluster-admin
  namespace: default
spec:
  clusterRef:
    name: my-cluster
    namespace: default
  token:
    permissions:
    - rules:
      - apiGroups: ["*"]
        resources: ["*"]
        verbs: ["*"]
```

```sh
kubectl apply -f ./hack/accessrequest.yaml
```

cluster-provider-k3d currently only provides admin service accounts.

Once granted, the kubeconfig is in a secret next to the AccessRequest:

```shell
kubectl wait --for='jsonpath={.status.phase}=Granted' accessrequest/my-cluster-admin --timeout=120s
kubectl get secret my-cluster-admin.kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d > my-cluster.kubeconfig
```

The kubeconfig points at the cluster's serverlb node name on the shared docker network — reachable from pods on the platform cluster, not from the host.

### Access from the host

The provisioned clusters are ordinary k3d clusters on the host's docker daemon:

```shell
k3d cluster list
k3d kubeconfig write <k3dClusterName>   # name from .status.providerStatus.k3dClusterName
```

### Deploy an OpenMCP managed cluster

Following <https://open-control-plane.io/users/getting-started/onboard>:

Create a Project:

```yaml
apiVersion: core.openmcp.cloud/v1alpha1
kind: Project
metadata:
  name: platform-team
  annotations:
    openmcp.cloud/display-name: Platform Team
spec:
  members:
    - kind: User
      name: system:admin
      roles:
        - admin
```

```shell
kubectl --kubeconfig onboarding.kubeconfig apply -f ./hack/project.yaml
```

And a Workspace inside:

```yaml
apiVersion: core.openmcp.cloud/v1alpha1
kind: Workspace
metadata:
  name: dev
  namespace: project-platform-team
  annotations:
    openmcp.cloud/display-name: Platform Team - Dev
spec:
  members:
    - kind: User
      name: system:admin
      roles:
        - admin
```

```shell
kubectl --kubeconfig onboarding.kubeconfig apply -f ./hack/workspace.yaml
```

And a ControlPLane:

```yaml
apiVersion: core.open-control-plane.io/v2alpha1
kind: ControlPlane
metadata:
  name: my-controlplane
  namespace: project-platform-team--ws-dev
spec:
  iam:
    oidc:
      defaultProvider:
        roleBindings:
        - roleRefs:
          - kind: ClusterRole
            name: cluster-admin
          subjects:
          - kind: User
            name: system:admin
    tokens:
    - name: ci-service-token
      roleRefs:
      - kind: ClusterRole
        name: cluster-admin
```

## Configuration

k3d supports a [config file](https://k3d.io/stable/usage/configfile/) in `K3D_CONFIG_FILE` to customize clusters.

If present the cluster-provider-k3d uses it to create clusters, but
name, wait behaviour and kubeconfig handling are always overridden to
the values openmcp/the provider expect.

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/cluster-provider-template/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/openmcp-project/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/cluster-provider-template/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/openmcp-project/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright OpenControlPlane contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-template).

---

<p align="center">
  <a href="https://apeirora.eu/content/projects/">
    <img alt="BMWK-EU funding logo" src="https://apeirora.eu/assets/img/BMWK-EU.png" width="300"/>
  </a>
</p>

<p align="center">
  OpenControlPlane is part of <a href="https://apeirora.eu/content/projects/">ApeiroRA</a>, an EU Important Project of Common European Interest (IPCEI-CIS).
</p>

<p align="center">
  Copyright Linux Foundation Europe. For web site terms of use, trademark policy and other project policies please see <a href="https://linuxfoundation.eu/en/policies">https://linuxfoundation.eu/en/policies</a>.
</p>
