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
make deploy IMG=<image>   # Deploy controller to cluster
make diff                 # Preview deployment changes
make samples              # Apply sample resources
```

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