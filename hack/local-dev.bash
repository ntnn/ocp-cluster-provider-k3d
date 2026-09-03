#!/usr/bin/env bash
#
# ocp on a k3d platform cluster with the k3d cluster provider installed.
# Requires docker, k3d, kubectl, task and go.

log() { echo ">>> $@"; }
die() { echo "$@" >&2; exit 1; }
installed() { command -v "$1" &> /dev/null; }

root=$(cd "$(dirname "$0")/.." && pwd)

require_tools() {
    for tool in docker k3d kubectl task go crane; do
        installed "$tool" || die "$tool is required"
    done
}

# renovate: datasource=github-releases depName=openmcp-project/openmcp-operator
OPENMCP_OPERATOR_VERSION=${OPENMCP_OPERATOR_VERSION:-v1.3.0}
OPENMCP_OPERATOR_IMAGE=${OPENMCP_OPERATOR_IMAGE:-ghcr.io/openmcp-project/images/openmcp-operator:${OPENMCP_OPERATOR_VERSION}}
OPENMCP_ENVIRONMENT=${OPENMCP_ENVIRONMENT:-debug}

# Defaults to the locally built image, see build_provider_image.
OPENMCP_CP_K3D_IMAGE=${OPENMCP_CP_K3D_IMAGE:-ghcr.io/openmcp-project/images/cluster-provider-k3d:$("$root/hack/common/get-version.sh")}

# Service providers and platform services, versions matching ocpctl's
# environment defaults (pkg/config/environment-defaults.yaml).
# renovate: datasource=docker registryUrl=https://ghcr.io depName=openmcp-project/images/service-provider-crossplane
SP_CROSSPLANE_IMAGE=${SP_CROSSPLANE_IMAGE:-ghcr.io/openmcp-project/images/service-provider-crossplane:v1.0.2}
# renovate: datasource=docker registryUrl=https://ghcr.io depName=openmcp-project/images/service-provider-flux
SP_FLUX_IMAGE=${SP_FLUX_IMAGE:-ghcr.io/openmcp-project/images/service-provider-flux:v1.1.0}
# renovate: datasource=docker registryUrl=https://ghcr.io depName=open-component-model/images/service-provider-ocm
SP_OCM_IMAGE=${SP_OCM_IMAGE:-ghcr.io/open-component-model/images/service-provider-ocm:v0.3.0}
# renovate: datasource=docker registryUrl=https://ghcr.io depName=openmcp-project/images/service-provider-kro
SP_KRO_IMAGE=${SP_KRO_IMAGE:-ghcr.io/openmcp-project/images/service-provider-kro:v1.1.0}
# renovate: datasource=docker registryUrl=https://ghcr.io depName=openmcp-project/images/platform-service-gateway
PS_GATEWAY_IMAGE=${PS_GATEWAY_IMAGE:-ghcr.io/openmcp-project/images/platform-service-gateway:v0.0.14}
# renovate: datasource=docker registryUrl=https://ghcr.io depName=openmcp-project/images/platform-service-project-workspace
# v2.4.0 enforces FIPS 140-3 and fails to verify k3s' ECDSA-signed CA, stay below until fixed upstream.
PS_PROJECT_WORKSPACE_IMAGE=${PS_PROJECT_WORKSPACE_IMAGE:-ghcr.io/openmcp-project/images/platform-service-project-workspace:v2.3.0}
FLUX2_INSTALL_URL=${FLUX2_INSTALL_URL:-https://github.com/fluxcd/flux2/releases/latest/download/install.yaml}
ENVOY_PROXY_IMAGE=${ENVOY_PROXY_IMAGE:-ghcr.io/openmcp-project/components/github.com/openmcp-project/openmcp/images/envoy-proxy:distroless-v1.36.2}
ENVOY_GATEWAY_IMAGE=${ENVOY_GATEWAY_IMAGE:-ghcr.io/openmcp-project/components/github.com/openmcp-project/openmcp/images/envoy-gateway:v1.5.4}
ENVOY_RATELIMIT_IMAGE=${ENVOY_RATELIMIT_IMAGE:-ghcr.io/openmcp-project/components/github.com/openmcp-project/openmcp/images/envoy-ratelimit:99d85510}
ENVOY_GATEWAY_CHART_URL=${ENVOY_GATEWAY_CHART_URL:-oci://ghcr.io/openmcp-project/components/github.com/openmcp-project/openmcp/charts/envoy-gateway}
ENVOY_GATEWAY_CHART_TAG=${ENVOY_GATEWAY_CHART_TAG:-1.5.4}

platform_cluster=platform
# Created k3d clusters join this network so that pods on the platform cluster
# can reach their API servers via container IP.
platform_network="k3d-${platform_cluster}"

create_platform_cluster() {
    if k3d cluster list "$platform_cluster" &> /dev/null; then
        log "platform cluster exists"
    else
        log "creating platform k3d cluster"
        # The host docker socket is mounted so the provider pod can drive k3d.
        # Disable Traefik, its bundled Gateway API CRDs conflict with the
        # envoy-gateway chart the gateway platform service installs.
        k3d cluster create "$platform_cluster" \
            --volume /var/run/docker.sock:/var/run/host-docker.sock@server:0 \
            --k3s-arg "--disable=traefik@server:*" \
            --wait \
            || die "failed to create platform cluster"
    fi

    kubeconfig=$(k3d kubeconfig write "$platform_cluster") || die "failed to write platform kubeconfig"
    export KUBECONFIG=$kubeconfig
}

build_provider_image() {
    test "${SKIP_BUILD:-false}" = "true" && return
    log "building provider image"
    (cd "$root" && task build:img:build-test) || die "failed to build provider image"
}

# ctr_import streams a single-platform image tarball into the k3d node's
# containerd. Not `k3d image import`: it imports with all platforms and trips
# over foreign-platform blobs and attestation manifests of multi-platform
# images (kind#3795, containerd#11344) - and exits 0 on node-side failure.
ctr_import() {
    platform=$1
    tarball=$2
    docker exec --privileged -i "k3d-${platform_cluster}-server-0" \
        ctr --namespace k8s.io images import --platform "$platform" --snapshotter=overlayfs - < "$tarball"
}

native_platform() {
    echo "linux/$(go env GOARCH)"
}

# import_registry_image pulls via crane into a single-platform tarball,
# bypassing the docker daemon: `docker save` with the containerd image store
# drops shared layer blobs of multi-platform images (moby#49473, kind#3795).
# Falls back to amd64 for images without a native-arch variant.
import_registry_image() {
    image=$1
    tarball=$(mktemp -t k3d-images-XXXXXX.tar) || die "failed to create temp file"
    platform=$(native_platform)
    if ! crane pull --platform "$platform" --format tarball "$image" "$tarball" 2> /dev/null; then
        platform=linux/amd64
        crane pull --platform "$platform" --format tarball "$image" "$tarball" \
            || { rm -f "$tarball"; die "failed to pull $image"; }
    fi
    ctr_import "$platform" "$tarball"
    status=$?
    rm -f "$tarball"
    test $status -eq 0 || die "failed to import $image"
}

# import_local_image streams a locally built image from the docker daemon;
# safe because a local single-arch build carries no manifest list.
import_local_image() {
    image=$1
    docker save "$image" | ctr_import "$(native_platform)" /dev/stdin || die "failed to import $image"
}

import_images() {
    log "importing images into platform cluster"
    import_registry_image "$OPENMCP_OPERATOR_IMAGE"
    import_local_image "$OPENMCP_CP_K3D_IMAGE"
}

deploy_openmcp_operator() {
    log "deploying openmcp-operator"
    kubectl apply -f - << EOF || die "failed to apply openmcp-operator resources"
apiVersion: v1
kind: Namespace
metadata:
  name: openmcp-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: openmcp-operator
  namespace: openmcp-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: openmcp-operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: openmcp-operator
  namespace: openmcp-system
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: openmcp-operator
  namespace: openmcp-system
data:
  config: |
    managedControlPlane:
      mcpClusterPurpose: mcp
      exposedEndpoints:
      - name: apiserver-external
      - name: apiserver-internal
    scheduler:
      scope: Cluster
      purposeMappings:
        mcp:
          template:
            spec:
              profile: k3d
              tenancy: Exclusive
        platform:
          template:
            spec:
              profile: k3d
              tenancy: Shared
        onboarding:
          template:
            spec:
              profile: k3d
              tenancy: Shared
        workload:
          template:
            spec:
              profile: k3d
              tenancy: Shared
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: openmcp-operator
  namespace: openmcp-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: openmcp-operator
  template:
    metadata:
      labels:
        app: openmcp-operator
    spec:
      serviceAccountName: openmcp-operator
      initContainers:
      - image: ${OPENMCP_OPERATOR_IMAGE}
        name: openmcp-operator-init
        args:
        - init
        - --environment
        - ${OPENMCP_ENVIRONMENT}
        - --config
        - /etc/openmcp-operator/config
        env:
        - name: POD_NAME
          valueFrom: {fieldRef: {fieldPath: metadata.name}}
        - name: POD_NAMESPACE
          valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
        - name: POD_IP
          valueFrom: {fieldRef: {fieldPath: status.podIP}}
        - name: POD_SERVICE_ACCOUNT_NAME
          valueFrom: {fieldRef: {fieldPath: spec.serviceAccountName}}
        volumeMounts:
        - name: config
          mountPath: /etc/openmcp-operator
          readOnly: true
      containers:
      - image: ${OPENMCP_OPERATOR_IMAGE}
        name: openmcp-operator
        args:
        - run
        - --environment
        - ${OPENMCP_ENVIRONMENT}
        - --config
        - /etc/openmcp-operator/config
        env:
        - name: POD_NAME
          valueFrom: {fieldRef: {fieldPath: metadata.name}}
        - name: POD_NAMESPACE
          valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
        - name: POD_IP
          valueFrom: {fieldRef: {fieldPath: status.podIP}}
        - name: POD_SERVICE_ACCOUNT_NAME
          valueFrom: {fieldRef: {fieldPath: spec.serviceAccountName}}
        volumeMounts:
        - name: config
          mountPath: /etc/openmcp-operator
          readOnly: true
      volumes:
      - name: config
        configMap:
          name: openmcp-operator
EOF
}

install_cluster_provider() {
    log "installing k3d cluster provider"
    kubectl wait --for=create customresourcedefinitions.apiextensions.k8s.io/clusterproviders.openmcp.cloud --timeout=60s \
        || die "clusterproviders CRD did not appear"
    # The gateway's envoy loadbalancer is exposed on the platform node
    # via servicelb. Alias the webhook hostname to it in every created
    # cluster so their API servers can call webhooks exposed through the
    # gateway.
    platform_node_ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"${platform_network}\").IPAddress}}" "k3d-${platform_cluster}-server-0") \
        || die "failed to determine platform node IP"
    kubectl apply -f - << EOF || die "failed to apply ClusterProvider"
apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster-provider-k3d
  namespace: openmcp-system
data:
  # SimpleConfig applied to every created cluster; join the platform network
  # so pods on the platform cluster can reach the created API servers, and
  # disable traefik (its Gateway API CRDs conflict with envoy-gateway).
  config.yaml: |
    apiVersion: k3d.io/v1alpha5
    kind: Simple
    network: ${platform_network}
    hostAliases:
      - ip: ${platform_node_ip}
        hostnames:
          - pwo-webhooks.platform.openmcp-system.openmcp.cluster.local
    options:
      k3s:
        extraArgs:
          - arg: --disable=traefik
            nodeFilters:
              - server:*
---
apiVersion: openmcp.cloud/v1alpha1
kind: ClusterProvider
metadata:
  name: k3d
spec:
  image: ${OPENMCP_CP_K3D_IMAGE}
  env:
  - name: K3D_CONFIG_FILE
    value: /etc/cluster-provider-k3d/config.yaml
  extraVolumes:
  - name: docker-socket
    hostPath:
      path: /var/run/host-docker.sock
      type: Socket
  - name: k3d-config
    configMap:
      name: cluster-provider-k3d
  extraVolumeMounts:
  - name: docker-socket
    mountPath: /var/run/docker.sock
  - name: k3d-config
    mountPath: /etc/cluster-provider-k3d
EOF
}

restart_provider() {
    kubectl get deployment cp-k3d -n openmcp-system &> /dev/null || return 0
    kubectl rollout restart deployment/cp-k3d -n openmcp-system || die "failed to restart provider deployment"
    kubectl rollout status deployment/cp-k3d -n openmcp-system --timeout=120s || die "provider deployment did not become ready"
}

create_provider_config() {
    log "creating ProviderConfig"
    kubectl wait --for=create customresourcedefinitions.apiextensions.k8s.io/providerconfigs.k3d.cluster.open-control-plane.io --timeout=120s \
        || die "providerconfigs CRD did not appear, check the cluster-provider-k3d-init job"
    kubectl apply -f - << EOF || die "failed to apply ProviderConfig"
apiVersion: k3d.cluster.open-control-plane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: k3d
spec: {}
EOF
}

create_platform_cluster_resource() {
    log "registering platform cluster"
    kubectl apply -f - << EOF || die "failed to apply platform Cluster"
apiVersion: clusters.openmcp.cloud/v1alpha1
kind: Cluster
metadata:
  name: platform
  namespace: openmcp-system
  annotations:
    # Adopt the already existing platform cluster instead of creating one.
    k3d.cluster.open-control-plane.io/name: ${platform_cluster}
spec:
  kubernetes: {}
  profile: k3d
  purposes:
  - platform
  tenancy: Shared
EOF
}

install_service_providers() {
    log "installing service providers"
    kubectl apply -f - << EOF || die "failed to apply service providers"
apiVersion: openmcp.cloud/v1alpha1
kind: ServiceProvider
metadata:
  name: crossplane
spec:
  image: ${SP_CROSSPLANE_IMAGE}
---
apiVersion: openmcp.cloud/v1alpha1
kind: ServiceProvider
metadata:
  name: flux
spec:
  image: ${SP_FLUX_IMAGE}
---
apiVersion: openmcp.cloud/v1alpha1
kind: ServiceProvider
metadata:
  name: ocm
spec:
  image: ${SP_OCM_IMAGE}
---
apiVersion: openmcp.cloud/v1alpha1
kind: ServiceProvider
metadata:
  name: kro
spec:
  image: ${SP_KRO_IMAGE}
---
apiVersion: openmcp.cloud/v1alpha1
kind: PlatformService
metadata:
  name: gateway
spec:
  image: ${PS_GATEWAY_IMAGE}
---
apiVersion: openmcp.cloud/v1alpha1
kind: PlatformService
metadata:
  name: project-workspace
spec:
  image: ${PS_PROJECT_WORKSPACE_IMAGE}
EOF
}

install_flux() {
    log "installing flux2 on the platform cluster"
    kubectl apply -f "$FLUX2_INSTALL_URL" > /dev/null || die "failed to install flux2"
}

configure_flux_provider() {
    log "configuring flux service provider"
    kubectl wait --for=create customresourcedefinitions.apiextensions.k8s.io/providerconfigs.flux.services.open-control-plane.io --timeout=120s \
        || die "flux providerconfigs CRD did not appear, check the sp-flux-init job"
    kubectl apply -f - << EOF || die "failed to apply flux ProviderConfig"
apiVersion: flux.services.open-control-plane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: flux
spec:
  versions:
    - version: "2.8.3"
      chartVersion: "2.18.2"
      chartUrl: "oci://ghcr.io/fluxcd-community/charts/flux2"
EOF
}

configure_gateway() {
    log "configuring gateway platform service"
    kubectl wait --for=create customresourcedefinitions.apiextensions.k8s.io/gatewayserviceconfigs.gateway.openmcp.cloud --timeout=120s \
        || die "gatewayserviceconfigs CRD did not appear, check the ps-gateway-init job"
    kubectl apply -f - << EOF || die "failed to apply GatewayServiceConfig"
apiVersion: gateway.openmcp.cloud/v1alpha1
kind: GatewayServiceConfig
metadata:
  name: gateway
spec:
  envoyGateway:
    images:
      proxy: "${ENVOY_PROXY_IMAGE}"
      gateway: "${ENVOY_GATEWAY_IMAGE}"
      rateLimit: "${ENVOY_RATELIMIT_IMAGE}"
    chart:
      url: "${ENVOY_GATEWAY_CHART_URL}"
      tag: "${ENVOY_GATEWAY_CHART_TAG}"
  clusters:
    - selector:
        matchPurpose: platform
    - selector:
        matchPurpose: workload
  dns:
    baseDomain: openmcp.cluster.local
EOF
}

# configure_project_workspace enables the Project/Workspace onboarding APIs.
# The init job creates the CRD but then requires this resource to exist and
# retries until it does.
configure_project_workspace() {
    log "configuring project-workspace platform service"
    kubectl wait --for=create customresourcedefinitions.apiextensions.k8s.io/projectworkspaceconfigs.core.openmcp.cloud --timeout=120s \
        || die "projectworkspaceconfigs CRD did not appear, check the ps-project-workspace-init job"
    kubectl apply -f - << EOF || die "failed to apply ProjectWorkspaceConfig"
apiVersion: core.openmcp.cloud/v1alpha1
kind: ProjectWorkspaceConfig
metadata:
  name: project-workspace
spec: {}
EOF
}

wait_for_onboarding_cluster() {
    log "waiting for onboarding cluster"
    kubectl wait --for=create -n openmcp-system cluster/onboarding --timeout=120s \
        || die "onboarding Cluster resource did not appear"
    kubectl wait --for='jsonpath={.status.phase}=Ready' -n openmcp-system cluster/onboarding --timeout=300s \
        || die "onboarding cluster did not become ready"
}

export_kubeconfigs() {
    k3d kubeconfig get platform > platform.kubeconfig
    log "platform cluster kubeconfig at $(pwd)/platform.kubeconfig"
    onboarding="$(kubectl --kubeconfig platform.kubeconfig -n openmcp-system get cluster onboarding  -o jsonpath='{.status.providerStatus.k3dClusterName}')"
    k3d kubeconfig get "$onboarding" > onboarding.kubeconfig
    log "onboarding cluster kubeconfig at $(pwd)/onboarding.kubeconfig"
}

deploy() {
    create_platform_cluster
    build_provider_image
    import_images
    deploy_openmcp_operator
    install_cluster_provider
    restart_provider
    create_provider_config
    create_platform_cluster_resource
    install_service_providers
    install_flux
    configure_flux_provider
    configure_gateway
    configure_project_workspace
    wait_for_onboarding_cluster
    export_kubeconfigs
    log "done - see README.md for how to request clusters"
}

reset() {
    if [ "${1:-}" != "--force" ]; then
        read -p "Delete ALL k3d clusters? (yes/no): " confirmation
        test "$confirmation" = "yes" || die "aborted"
    fi
    k3d cluster delete --all || die "failed to delete clusters"
}

usage() {
    cat << EOF
Usage: $(basename "$0") <command>

Commands:
    deploy           Deploy the openMCP environment with the k3d provider
    reset [--force]  Delete all k3d clusters
EOF
}

test $# -eq 0 && { usage; exit 0; }

subcmd="$1"
shift

case "$subcmd" in
    (deploy) require_tools; deploy;;
    (kubeconfigs) require_tools; export_kubeconfigs;;
    (reset) require_tools; reset "$@";;
    (help|-h|--help) usage;;
    (*) die "Unknown subcommand: $subcmd";;
esac
