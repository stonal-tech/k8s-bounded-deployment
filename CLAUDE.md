# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Kubernetes controller that implements a `BoundedDeployment` CRD, providing pod management with configurable minimum and maximum replica bounds. The controller is built using Kubebuilder v4 and Controller Runtime.

## Common Development Commands

### Build and Run
```bash
make build                # Build manager binary
make run                  # Run controller locally (requires kubeconfig)
make docker-build         # Build Docker image
```

### Code Quality
```bash
make fmt                  # Format Go code
make vet                  # Run go vet
make lint                 # Run golangci-lint
make lint-fix             # Run golangci-lint with auto-fixes
```

### Testing
```bash
make test                 # Run unit tests with coverage
make test-e2e             # Run end-to-end tests (requires Kind cluster)
```

### Code Generation
```bash
make manifests            # Generate CRDs and RBAC
make generate             # Generate DeepCopy methods
```

### Deployment
```bash
make install              # Install CRDs to cluster
make deploy               # Deploy controller (pulls $(IMG) from GHCR)
make diff                 # Preview deployment changes against the current kube context
make template             # Render the manifests that would be deployed (kustomize build) to stdout
make samples              # Apply sample resources
```

**Image resolution.** `IMG` defaults to `$(REGISTRY)/k8s-bounded-deployment:$(VERSION)`:
- `REGISTRY` is `ghcr.io/stonal-tech` for every cluster. Images come from `.github/workflows/release.yml`, which publishes a multi-arch image on each `v*` tag. There is no per-provider registry lookup any more — the old `PROVIDER` probe of the `stonal-system/cluster-info` ConfigMap (AWS ECR vs `registry.stonal-secnum.io`) is gone, so no target makes a cluster call to resolve `IMG`.
- `VERSION` defaults to `0.23.0`. **No leading `v`**: `docker/metadata-action`'s `{{version}}` pattern strips it, so the tags on GHCR are `0.23.0`, `0.23` and `latest` — `v0.23.0` does not exist and will fail to pull.
- `REGISTRY`, `VERSION`, or `IMG` can be overridden on the command line (e.g. `make diff VERSION=0.24.0`).

**GHCR package visibility.** The `stonal-tech/k8s-bounded-deployment` package must stay **public** for clusters to pull it without credentials. It was created `internal`, which rejects anonymous pull tokens; if a pull starts failing with `UNAUTHORIZED`, check the package's visibility before anything else. The alternative is an imagePullSecret holding a PAT with `read:packages` in the controller's namespace.

**Kustomize image override gotcha.** `config/manager/manager.yaml` must reference the image as `controller:latest` (the kubebuilder alias). The override in `config/manager/kustomization.yaml` keys on `name: controller` — if the manifest is changed to a literal registry path, the override silently no-ops and `make deploy` pushes `:latest` regardless of `IMG`.

## Architecture

### Core Components

**API Types** (`api/v1/`):
- `BoundedDeployment`: Main CRD with spec fields for `replicas` (min), `maxReplicas` (max), `margin`, and `template`
- Uses controller-gen for code generation

**Controllers** (`internal/controller/`):
- `BoundedDeploymentReconciler`: Manages BoundedDeployment lifecycle, pod creation/deletion
- `DeploymentController`: Optional controller to watch standard Deployments and create BoundedDeployments
- Uses Controller Runtime reconciler pattern with exponential backoff

**Main Application** (`cmd/main.go`):
- Sets up manager with metrics server (`:8080`), health endpoint (`:8081`), and leader election
- Configures both BoundedDeployment and Deployment controllers

### Key Patterns

- **Template Hash Management**: Controller computes hash of pod template to detect changes
- **Pod Ownership**: Pods are owned by BoundedDeployment for garbage collection
- **Status Reporting**: Updates `.status.replicas` and `.status.readyReplicas` fields
- **Resource Scaling**: Respects bounds when scaling between min/max replicas

## Testing Framework

Uses Ginkgo/Gomega testing framework:
- Unit tests in `*_controller_test.go` files use envtest for Kubernetes API simulation
- E2E tests in `test/e2e/` run against real clusters
- Test environment uses Kubernetes 1.31.0 (configurable via `ENVTEST_K8S_VERSION`)

## Development Environment

- **Go Version**: 1.24.4
- **Kubernetes APIs**: v0.33.1
- **Controller Runtime**: v0.21.0
- **Tools**: Auto-downloaded to `./bin/` (kustomize, controller-gen, envtest, golangci-lint)

## Useful Monitoring Command

```bash
# Watch BoundedDeployments with status
kubectl get boundeddeployments -A -w -o jsonpath='{.metadata.namespace}:{.metadata.name} - SPEC: replicas:{.spec.replicas}, maxReplicas:{.spec.maxReplicas}{"  -  "}STATUS: replicas:{.status.replicas}, readyReplicas:{.status.readyReplicas}{"\n"}'
```