## Add owner reference

Your controller must set owner references on the pods it creates. This establishes the parent-child relationship:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  ownerReferences:
  - apiVersion: deploy.stonal.io/v1
    kind: BoundedDeployment
    name: my-boundeddeployment
    uid: <boundeddeployment-uid>
    controller: true
```

In your controller code, when creating pods:
```go
import (
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
    v1 "github.com/stonal-tech/k8s-bounded-deployment/api/v1"
)
// ...
if err := controllerutil.SetControllerReference(boundedDep, newPod, r.Scheme); err != nil {
    // handle error
}
```

## Add CRD with status subresource
Add a status subresource to track managed pods:
```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  # ... existing spec
  subresources:
    status: {}
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              replicas:
                type: integer
              maxReplicas:
                type: integer
              margin:
                type: integer
              template:
                x-kubernetes-preserve-unknown-fields: true
                description: Template for the pods to start
          status:
            type: object
            properties:
              replicas:
                type: integer
              readyReplicas:
                type: integer
              templateHash:
                type: string
```

## Implement Proper Status Updates

Your controller should update the status with pod information:

```go
boundedDep.Status.Replicas = len(activePods)
boundedDep.Status.ReadyReplicas = countNbReadyReplicas(activePods)
boundedDep.Status.TemplateHash = templateHash
```

## Use consistent labels and annotations

Ensure your pods have consistent labels and annotations that match your selector and controller conventions:

```yaml
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: my-app
    deploy.stonal.io/boundeddeployment: my-boundeddeployment
  annotations:
    deploy.stonal.io/template-hash: <hash>
spec:
  # ... pod spec
```

## Reference sample CR

See `config/samples/deploy_v1_boundeddeployment.yaml` for up-to-date examples of BoundedDeployment usage and field conventions.
