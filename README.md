# Operator

The Grey Matter Operator enables a bootstrapped mesh deployment using the `greymatter.io/v1.Mesh`
CRD to manage mesh deployments in a Kubernetes cluster.

## Prerequisites

- [kubectl v1.23+](https://kubernetes.io/docs/tasks/tools/)
  - Cluster administrative access
- [CUE CLI](https://cuelang.org/docs/install/)

> NOTE: This project makes use of git submodules for dependency management.

## Getting Started

Make sure you have fetched all necessary dependencies:

```bash
./scripts/bootstrap # checks that you have the latest dependencies for the cue evaluation of manifests.
```

Then evaluate the kubernetes manifests using CUE:

Note: You may run the operator locally entirely without GitOps (i.e., using only baked-in, local CUE config) by adding
`-t test=true` to the `cue eval ...`. At the moment, you will still need to create the greymatter-sync-secret as below,
or else remove the references to it in the operator manifests, but it won't be used with `-t test=true`.

```bash
(
# CUE config root
cd pkg/cuemodule/core

# Necessary only so we can create the secrets before launching the operator.
# The operator manifests create this namespace if it doesn't already exist.
kubectl create namespace gm-operator

# Image pull secret
kubectl create secret docker-registry gm-docker-secret \
  --docker-server=quay.io \
  --docker-username=$QUAY_USERNAME \
  --docker-password=$QUAY_PASSWORD \
  -n gm-operator

# GitOps SSH key
# EDIT THIS to reflect your own, or some other SSH private key with access,
# to the repository you would like the operator to use for GitOps. Note
# that by default, the operator is going to fetch from 
# https://github.com/greymatter-io/gitops-core and you would
# need to edit the operator StatefulSet to change the argument to the
# operator binary to change the git repository or branch.
kubectl create secret generic greymatter-sync-secret \
  --from-file=id_ed25519=$HOME/.ssh/id_ed25519 \
  -n gm-operator

# Operator installation and supporting manifests
cue eval -c ./k8s/outputs --out text \
         -t operator_image=quay.io/greymatterio/operator:0.9.3 \
         -e operator_manifests_yaml | kubectl apply -f -
)
```

> HINT: Your username and password are your Quay.io credentials authorized to the greymatterio organization.

The operator will be running in a pod in the `gm-operator` namespace.

That is all you need to do to launch the operator. Note that if you have the spire config flag set
(in pkg/cuemodule/core/inputs.cue) then you will need to wait for the operator to insert the server-ca bootstrap certificates
before spire-server and spire-agent can successfully launch.

## Deployment Assist

The operator can assist with deployments by injecting and configuring a sidecar with an HTTP ingress, given only a
correctly-annotated Deployment or StatefulSet. This makes use of the following two annotations, which need to be in
`spec.template.metadata.annotations`. (NB: they must go in the _template_ metadata, not the metadata of the Deployment
or StatefulSet itself. This is because the Pod itself must have those annotations available for the webhooks to
inspect.)

```yaml
greymatter.io/inject-sidecar-to: "3000"    # (a)
greymatter.io/configure-sidecar: "true"    # (b)
```

The meaning of the above is
a) inject a sidecar into this pod that (if configured) will forward traffic upstream to port 3000.
b) configure the injected sidecar (i.e., send Grey Matter configuration to Control API that configures an HTTP ingress,
   a metrics beacon to Redis for health checks, Spire configuration if applicable, and a Catalog service entry.

For clarity, here is a full working example of a Deployment that will receive a sidecar injection and Grey Matter
configuration. Once it has been applied, the operator logs should show its configuration, it should appear with two
containers in the pod, and there should shortly afterwards be a working service in the mesh with a card on the
dashboard:

```yaml
# workload.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: simple-server
spec:
  selector:
    matchLabels:
      app: simple-server
  template:
    metadata:
      labels:
        app: simple-server
      annotations:
        greymatter.io/inject-sidecar-to: "3000"
        greymatter.io/configure-sidecar: "true"
    spec:
      containers:
        - name: server1
          image: python:3
          command: ["python"]
          args: ["-m", "http.server", "3000"]

```

## Alternative Debug Build

If you are an (internal) developer or SRE working on or with the operator,
you will likely want to use the debug build. This build comes bundled with
a [debugger](https://github.com/go-delve/delve) and [bugsnag](https://www.bugsnag.com/), a library that automatically reports the occurance
of fatal errors.

Debug builds are marked with the debug prefix tag. For example:

- debug-latest
- debug-0.9.2

You can also build images locally with `./scripts/build debug_container`.

The debug image requires an api token stored in a secret. Create it before
launching the operator and make sure to update the image tag in the `cue eval`.

If all went well you should see "Initializing Bugsnag" in the operator logs.

Installation with the debug container:

```bash
(
# CUE config root
cd pkg/cuemodule/core

# Necessary only so we can create the secrets before launching the operator.
# The operator manifests create this namespace if it doesn't already exist.
kubectl create namespace gm-operator

# Image pull secret
kubectl create secret docker-registry gm-docker-secret \
  --docker-server=quay.io \
  --docker-username=$QUAY_USERNAME \
  --docker-password=$QUAY_PASSWORD \
  -n gm-operator

# GitOps SSH key
# EDIT THIS to reflect your own, or some other SSH private key with access,
# to the repository you would like the operator to use for GitOps. Note
# that by default, the operator is going to fetch from 
# https://github.com/greymatter-io/gitops-core and you would
# need to edit the operator StatefulSet to change the argument to the
# operator binary to change the git repository or branch.
kubectl create secret generic greymatter-sync-secret \
  --from-file=id_ed25519=$HOME/.ssh/id_ed25519 \
  -n gm-operator

#Bugsnag API Token - Search vault for the token value
 kubectl create secret generic bugsnag-api-token \
  --from-literal=token=$BUGSNAG_API_TOKEN \
  -n gm-operator

# Operator installation and supporting manifests
cue eval -c ./k8s/outputs --out text \
         -t operator_image=quay.io/greymatterio/operator:debug-0.9.3 \
         -t debug=true \
         -e operator_manifests_yaml | kubectl apply -f -
)
  
# To connect, first port-forward to 2345 on the operator container in a separate terminal window
kubectl port-forward sts/gm-operator 2345 -n gm-operator
```

Now you can connect GoLand or VS Code or just vanilla Delve to localhost:2345 for debugging.

> Note: Delve is not configured to halt the operator at startup. If you need to debug the operator
> from startup, remove the `--continue` argument from the StatefulSet args array.

## Inspecting Manifests

The following commands print out manifests that can be applied to a Kubernetes cluster, for your inspection:

```bash
( 
  cd pkg/cuemodule/core
  cue eval -c ./k8s/outputs --out text -e spire_manifests_yaml
)

# pick which manifests you'd like to inspect

(
  cd pkg/cuemodule/core
  cue eval -c ./k8s/outputs --out text -e operator_manifests_yaml
)
```

OR with Kustomize:

```bash
kubectl kustomize config/context/kubernetes
```
>NOTE: If deploying to OpenShift, you can 
> replace `config/context/kubernetes` with `config/context/openshift`.)

## Using nix-shell

For those using the [Nix package manager](https://nixos.org/download.html), a `shell.nix` script has
been provided at the root of this project to launch the operator in a local
[KinD](https://kind.sigs.k8s.io/) cluster.

Some caveats:
* You should have Docker and Nix installed
* You should be able to log in to `quay.io`

To launch, simply run:
```bash
nix-shell
```


# Development

## Prerequisites

- [Go v1.17+](https://golang.org/dl/)
- [Operator SDK v1.12+](https://sdk.operatorframework.io)
- Grey Matter CLI 
- [CUE CLI](https://cuelang.org/docs/install/)
- [staticcheck](https://staticcheck.io/)
- git

If building for
[OpenShift](https://www.redhat.com/en/technologies/cloud-computing/openshift/container-platform),
you'll also need the [OpenShift
CLI](https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/).

## Setup

Verify all dependency installations and update CUE modules:

```bash
./scripts/bootstrap
```
