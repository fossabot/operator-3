# Changelog

## 0.9.3 (August 11, 2022)

### Added

- Added support for using a tag as a GitOps target with `-tag`.

## 0.9.2 (July 15, 2022)

### Changed

- Upgraded the packaged greymatter CLI version to 4.2.
- Updated submodules

## 0.9.1 (June 16, 2022)

### Changed

- The annotations for deployment assist now need to be put in `spec.template.metadata.annotations`,
  not the top-level `metadata.annotations` for the Deployment or StatefulSet.

### Added

- Watched namespaces that do not already exist are created before the imagePullSecret for
  retrieving the Grey Matter sidecar is copied in.

### Fixed

- A sidecar is no longer injected if the inject-sidecar-to annotation is missing from a Deployment
  or StatefulSet in a watched namespace.

## 0.9.0 (June 13, 2022)

### Changed

- Completely new CUE for both Kubernetes manifests and Grey Matter config, rooted under
  `pkg/cuemodule/core` (`inputs.cue`, and `gm/` and `k8s/` subdirectories) following a philosophy of
  readability and malleability.
- Operator Go now loads CUE generically, such that nearly all changes to deployment configuration
  require no changes to the Go.
- Launch the operator with CUE-generated manifests, which also include the Spire manifests if the
  spire config flag is set.
- Deployment assist is now opt-in, with labels `"greymatter.io/inject-sidecar-to": "<port num>"`
  (default is to omit the label) and `"greymatter.io/configure-sidecar": "<boolean>"` (default
  "true".)
- Operator itself now runs as a StatefulSet rather than a Deployment.
- One-Mesh/One-Operator - Multiple meshes require multiple operators (they do yet not play nicely
  together, but the target for multi-mesh support is to support graceful interop.)
- Added a flag (`auto_apply_mesh`) that causes the operator to automatically deploy the default mesh
  defined in the CUE, after a delay. Turn the flag off to manually deploy a Mesh CR.
- Switch to ECDSA 256 certificates for speed and security.
- Checked in `pkg/cuemodule/core/cue.mod/gen/k8s.io/`, the result of `cue get go k8s.io/api/...`,
  for the CI build.
- GitOps workflow now fetches the operator's CUE from a repository (default greymatter-io/gitops-core)
  and polls it for changes which then trigger live updates to the Kubernetes and Grey Matter config
  of the live environment.

### Added

- Added an annotation to control whether sidecar configuration is created for a given Deployment
  or StatefulSet, e.g., `greymatter.io/configure-sidecar: "false"`
- Add a debug container build mode and instructions
- OpenShift support restored, with automatic permissioning for core Grey Matter components

### Fixed

- Fixed parsing of the `watch_namespaces` field in the Mesh custom resource `spec` that prevented
  Control's `GM_CONTROL_KUBERNETES_NAMESPACES` environment variable from being set, causing service
  discovery to fail to identify data planes in the `install_namespace` when no `watch_namespaces`
  were specified.
- Cleaned up unnecessary SPIRE agent unix workload attestor configuration.
- Fixed family of issues that prevented the operator from restarting cleanly. The Operator now
  reloads the list of sidecars used for Spire mTLS authorization from the Mesh CR itself.
- Fixed control-api URL in dashboard so that proxy config inspection and business impact selection
  would work from the UI.

## 0.3.3 (February 10, 2022)

This release includes internal changes to how we source Grey Matter mesh configuration schema.
Grey Matter mesh configurations are defined in CUE and bundled into this project via a Git
submodule [greymatter-cue](https://github.com/greymatter-io/greymatter-cue). Mesh custom resources
are also ingested into CUE to be parsed for installing and configuring mesh deployments.

Additionally, this release addressed a bug that prevented the operator from restoring its own
internal state of existing Mesh custom resources on startup. The fix ensures that the leader
replica of the operator will always have the latest state of Mesh custom resources.

This release also has been tested for local development on Rancher Desktop.

### Changed

- Moved all CUE packages into a single CUE module that lives in `pkg/cuemodule/core`. Each CUE package
  may be evaluated using the CUE CLI as well as loaded via exported loader functions defined in the
  `cuemodule` Go package.

- Updated versions of Spire images (spire-server, spire-agent, and the k8s-workload-registrar) that
  the operator installs to 1.2.0. This fixes an issue on some platforms where Spire would
  misidentify the process as non-k8s, and fail to issue an identity.

- Specifying `--platform linux/amd64` in docker and buildah container builds to ensure M1
  Macs know what to do with it, regardless of where it was built.

- Increased memory limit (100Mi -> 300Mi) and increased initial wait period on readiness and
  liveness probes to accommodate slower/emulated machines (-> 120 sec).

### Added

- [greymatter-cue](https://github.com/greymatter-io/greymatter-cue) Git submodule for importing
  Grey Matter CUE schemas.
- Generated CUE schema for the Mesh custom resource definition, to be parsed within CUE when
  generating installation manifests and mesh configurations for a configured Mesh.
- Fix concurrent write to a map shared among goroutines.

### Fixed

- Updated flag in spire server liveness probe to prevent an eventual crash of spire server.
- Addressed a bug that kept existing Mesh custom resources from being fully registered with a new
  instance of the operator on startup. The bug prevented new mesh configurations from being
  generated for workloads within namespaces of existing meshes.

## 0.3.0 (January 10, 2022)

### Added

- SPIRE as SPIFFE implementation for mutual TLS between workloads

### Fixed

- Prevent `greymatter apply` commands to Control API from running until it has connected to Redis.

### Removed

- Support for an external Redis cache for mesh configurations, opting for an internally-managed one
  secured by mutual TLS (via SPIRE).
- Catalog entry for Redis
- JWT Security service's Redis dependency
- Listeners using port 10707 for initial boootstrapping of mesh configuration

## 0.2.0 (December 10, 2021)

### Changed

- Upgrade Grey Matter CLI binary dependency from 3.0.0 to 4.0.1

## 0.1.2 (December 2, 2021)

### Changed

- Change `GREYMATTER_DOCKER_*` config env vars to `GREYMATTER_REGISTRY_*`
- Change k8s-operator `--username` and `--password` flags to `--registry-username`
  and `--registry-password`, respectively. And remove -u and -p aliases to
  reserve these for future use.

## 0.1.0 (December 1, 2021)

This is a pre-release with basic support for installing Grey Matter core 
components and dependencies and bootstrapping Grey Matter mesh configurations.

### Added

- Support for general Kubernetes distributions
- Support for OpenShift, packaged for compatibility with the Operator Lifecycle Manager
