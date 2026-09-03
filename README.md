[![REUSE status](https://api.reuse.software/badge/github.com/openmcp-project/cluster-provider-template)](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-template)

# cluster-provider-template

## About this project

A template for building @openmcp-project Cluster Providers.

## Requirements and Setup

1. Create a new repository based on this template.
2. Install [opencontrolplane-gen](https://github.com/openmcp-project/opencontrolplane-gen).
3. Use `task template:generate-provider` to create a new Cluster Provider.
4. Test your Cluster Provider with `task test-e2e`.

The template generates a basic Cluster Provider with the following 3 Controllers:

- [accessrequest/controller.go](./internal/controller/accessrequest/controller.go): Reconcile `AccessRequest` resources that have been assigned to your Cluster Provider.
- [cluster/controller.go](./internal/controller/cluster/controller.go): Reconcile `Cluster` resources that have been assigned to your Cluster Provider.
- [config/controller.go](./internal/controller/config/controller.go): Create `ClusterProfile` resources based on the `ProviderConfig` of your Cluster Provider.

For a detailed guide on setup and usage, please refer to the full [Cluster Provider Development Guide](https://open-control-plane.io/developers/clusterprovider/develop).

## Template Taskfiles

This template contains two Taskfiles:

- Taskfile.yaml contains the tasks to use once you created a Cluster Provider based on this template.
- Taskfile_template.yaml contains the tasks to use while working with the template. This Taskfile can be removed once you used this template to create a Cluster Provider.

The following sections give a brief overview of the template specific tasks.

### User tasks

To generate a new Cluster Provider, use `task template:generate-provider`:

```shell
task template:generate-provider name=foo module=github.com/yourorg/cluster-provider-foo
```

Add `dryrun=true` to print the result without applying the changes to disk.

`template:generate-provider` supports the following arguments:

- `dryrun`: Print in-memory result to stdout without altering any files (default false)
- `name`: Name of the Cluster Provider (default "example")
- `module` The go module name of your Cluster Provider (default "github.com/openmcp-project/cluster-provider-example")

### Development tasks

The following tasks are useful to test any template code changes.

- `template:dev:gen`: Executes the template with the default values to render "cluster-provider-example" for local development.
- `template:dev:img`: Builds a container image for "cluster-provider-example". This also includes code validating.
- `template:dev:e2e`: Executes e2e tests for "cluster-provider-example".

All `template:dev` tasks support the following arguments:

- `debug`: enables debug logs of [opencontrolplane-gen](https://github.com/openmcp-project/opencontrolplane-gen).

## Cluster Provider Runtime Flags

The generated platform service supports the following runtime flags:

- `--verbosity`: Logging verbosity level (see [controller-runtime logging](https://github.com/kubernetes-sigs/controller-runtime/blob/main/TMP-LOGGING.md))
- `--environment`: Name of the environment (required for operation)
- `--provider-name`: Name of the provider resource (required for operation)
- `--metrics-bind-address`: Address for the metrics endpoint (default: `0`, use `:8443` for HTTPS or `:8080` for HTTP)
- `--health-probe-bind-address`: Address for health probe endpoint (default: `:8081`)
- `--leader-elect`: Enable leader election for controller manager (default: `false`)
- `--metrics-secure`: Serve metrics endpoint securely via HTTPS (default: `true`)
- `--enable-http2`: Enable HTTP/2 for metrics and webhook servers (default: `false`)

For a complete list of available flags, run the generated binary with `-h` or `--help`.

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
