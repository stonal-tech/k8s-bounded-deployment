# k8s-bounded-deployment

A Kubernetes controller providing the `BoundedDeployment` custom resource: a pod count with
a floor and a ceiling, where scale-down happens by letting pods finish rather than by
terminating them.

It is built for workers that process a unit of work and exit — queue consumers, batch and
ETL jobs, importers, indexers. It is not a `Deployment` replacement; see
[Limitations](#limitations).

![Pod-count timeline showing the floor, the ceiling, and the hands-off band between them](docs/assets/bounded-band.svg)

## How it works

| Running pods | Controller action |
| --- | --- |
| Below `spec.replicas` | Create one pod per reconcile until the floor is met |
| Between floor and ceiling | None |
| Above the ceiling | Delete the newest pod, one per reconcile |

- The floor is `spec.replicas`. Unlike a `Deployment`, it is a minimum, not a target.
- The ceiling is `spec.maxReplicas`, or `spec.replicas + spec.margin` when `maxReplicas` is
  unset. If neither is set there is no ceiling.
- Pods that reach `Succeeded` or `Failed` are deleted. If that drops the count below the
  floor, a replacement is created.
- Over the ceiling, the newest pod is deleted first — the one with the least work invested.

The gap between floor and ceiling is how many surplus pods you tolerate while waiting for
them to finish. Lowering the floor from 10 to 2 with `margin: 3` sets the ceiling to 5:
three pods are deleted one per reconcile, and the remaining five drain to 2 as they finish.
With `margin: 0` the ceiling equals the floor, so every scale-down terminates pods
immediately.

## Compared to an HPA

![Side-by-side comparison of the same scale-down event under HPA and under BoundedDeployment](docs/assets/hpa-vs-bounded.svg)

| | HPA + Deployment | BoundedDeployment |
| --- | --- | --- |
| Scale-down | Terminates pods chosen by replica count, regardless of what they are doing | Pods exit when their work is done and are not replaced |
| Scale signal | Metrics the HPA can read, usually CPU or memory | Whatever patches `spec.replicas` |
| Minimum | `minReplicas` is at least 1 unless the `HPAScaleToZero` feature gate is enabled | `0` by default |
| Convergence | 10% tolerance and a 300s scale-down stabilization window by default | Exact count, no tolerance |

An HPA remains the better choice for stateless request handlers, where pods are
interchangeable and CPU tracks load.

## Scaling from zero

`spec.replicas: 0` is the default and a valid resting state: no pods run, while the
resource and its template remain.

![Timeline of a burst handled from zero by a BoundedDeployment and by an HPA](docs/assets/scale-to-zero.svg)

```bash
kubectl patch boundeddeployment semantic-indexer -n dev \
  --type merge -p '{"spec":{"replicas":8}}'
```

The count reaches exactly 8. The controller watches the pods it owns, so each pod creation
triggers the next reconcile; there is no polling interval or metrics scrape in the path.

Returning to zero has two forms:

| | Set | Result |
| --- | --- | --- |
| Drain | `replicas: 0`, `margin: N` | Up to `N` pods keep running until they finish |
| Stop | `replicas: 0`, `maxReplicas: 0` | All pods are deleted immediately |

## Requirement: pods must exit

![Pod state machine from Pending through Running to Succeeded or Failed, then reaped](docs/assets/pod-lifecycle.svg)

A pod is expected to exit 0 when its batch is done, when the queue is empty, or after a
fixed lifetime. A pod that runs forever is never collected, so the count never drops and
scale-down never happens.

The controller rewrites `restartPolicy: Always` to `OnFailure` on the resource. Under
`Always` the kubelet restarts a cleanly exited container, so the pod never reaches
`Succeeded` and the controller never sees it finish.

## Install

The controller is one CRD plus one deployment, installed into the
`k8s-bounded-deployment-system` namespace.

```bash
kubectl apply -f https://github.com/stonal-tech/k8s-bounded-deployment/releases/latest/download/install.yaml
```

From source:

```bash
make install                                                          # CRD only
make deploy IMG=ghcr.io/stonal-tech/k8s-bounded-deployment:latest     # + controller
```

To use your own registry, run `make docker-build docker-push IMG=<registry>/<name>:<tag>`
and pass the same `IMG` to `make deploy`. Uninstall with `make undeploy && make uninstall`.

## API reference

```yaml
apiVersion: deploy.stonal.io/v1
kind: BoundedDeployment
metadata:
  name: semantic-indexer
spec:
  replicas: 2      # floor
  margin: 3        # ceiling = 5
  template:
    metadata:
      labels:
        app: semantic-indexer
    spec:
      restartPolicy: OnFailure
      containers:
        - name: worker
          image: ghcr.io/example/semantic-indexer:1.4.0
```

### Spec

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `replicas` | integer | no, defaults to `0` | The floor |
| `maxReplicas` | integer | no | Absolute ceiling. Must be at least `replicas` |
| `margin` | integer | no | Ceiling as a distance above the floor. Ignored when `maxReplicas` is set |
| `template` | PodTemplateSpec | yes | The pod to run. Must declare at least one container |

### Status

| Field | Meaning |
| --- | --- |
| `replicas` | Pods alive and not terminating |
| `readyReplicas` | Of those, how many pass their readiness probe |
| `templateHash` | Hash of the current pod template |

```
$ kubectl get bd

NAME               MIN   MAX   MARGIN   CURRENT   READY
semantic-indexer   2           3        4         4
```

### Pod metadata

Pods are named `<name>-bounded-<suffix>`, labelled
`deploy.stonal.io/boundeddeployment: <name>`, and annotated with
`deploy.stonal.io/template-hash`. The label is how the controller finds pods, so do not
override it in the template. Owner references are set, so deleting the `BoundedDeployment`
deletes its pods.

## Driving it

The controller reconciles toward the floor; setting the floor is external to it.

![Integration diagram: work source, scaler, custom resource, controller and worker pods](docs/assets/components.svg)

Anything that can `PATCH` a custom resource can drive it — a CronJob, an application that
knows its own backlog, or a queue-depth watcher.

[KEDA](https://keda.sh) drives scale targets through the `/scale` subresource, which is not
enabled on this CRD; see [Limitations](#limitations).

## Adopting an existing Deployment

A second controller converts an annotated `Deployment`:

```yaml
metadata:
  annotations:
    stonal.io/bounded-enabled: "true"
    stonal.io/bounded-margin: "3"     # or stonal.io/bounded-max: "10"
```

It creates a `BoundedDeployment` with the same name, template and replica count, then
scales the `Deployment` to zero. Either `bounded-margin` or `bounded-max` enables it;
`bounded-enabled: "false"` disables it. The conversion runs once — later edits to the
`Deployment` are not propagated.

## Operating

```bash
kubectl get pods -l deploy.stonal.io/boundeddeployment=semantic-indexer
kubectl -n k8s-bounded-deployment-system logs deploy/k8s-bounded-deployment-controller-manager -f
```

Every create, delete and reap is logged with its reason.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--health-probe-bind-address` | `:8081` | Liveness and readiness endpoints |
| `--metrics-bind-address` | `0` (disabled) | `:8443` for HTTPS, `:8080` for HTTP |
| `--metrics-secure` | `true` | Serve metrics over HTTPS with authn/authz |
| `--leader-elect` | `false` | Leader election for HA |
| `--enable-http2` | `false` | Off by default, for the HTTP/2 Rapid Reset CVEs |

## Limitations

- **No rolling update.** On a template change, every pod whose hash no longer matches is
  deleted in the same pass, and replacements are created one per reconcile. There is no
  surge, no `maxUnavailable` and no PodDisruptionBudget integration.
- **No `/scale` subresource**, so `kubectl scale` and KEDA's generic scaler cannot target
  it. Use `kubectl patch`.
- **The Deployment adoption controller is one-shot**: it creates the `BoundedDeployment`
  and never updates it, while keeping the `Deployment` scaled to zero.
- **No status conditions**, so `kubectl wait --for=condition=...` has nothing to wait on.
- **No scaling logic.** There is no metrics adapter or queue integration in this repo.
- **The test suite is largely Kubebuilder scaffolding** and asserts little about the
  reconcile logic.

## Development

Requires Go 1.24+. Tooling is downloaded into `./bin` on demand.

```bash
make build          # build the manager binary
make run            # run against the current kubeconfig
make test           # unit tests via envtest
make test-e2e       # end-to-end tests, needs a Kind cluster
make lint           # golangci-lint
make manifests generate   # regenerate CRDs, RBAC and DeepCopy methods
```

Samples are in `config/samples`, applied with `make samples`. Built with
[Kubebuilder](https://book.kubebuilder.io) v4 and
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).

## License

Apache 2.0 — see [LICENSE](LICENSE).
