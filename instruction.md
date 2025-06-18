## Add owner reference

Your controller must set owner references on the pods it creates. This establishes the parent-child relationship:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  ownerReferences:
  - apiVersion: your-api-group/v1
    kind: BoundedDeployment
    name: my-boundeddeployment
    uid: <boundeddeployment-uid>
    controller: true
```

In your controller code, when creating pods:
```go
// Example in Go
pod := &corev1.Pod{
    ObjectMeta: metav1.ObjectMeta{
        Name:      podName,
        Namespace: boundedDeployment.Namespace,
        OwnerReferences: []metav1.OwnerReference{
            *metav1.NewControllerRef(boundedDeployment, schema.GroupVersionKind{
                Group:   "your-api-group",
                Version: "v1",
                Kind:    "BoundedDeployment",
            }),
        },
    },
    // ... rest of pod spec
}
```

## Add CRD with status subresources
Add a status subresource to track managed pods:
```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  # ... existing spec
  subresources:
    status: {}
    scale:
      specReplicasPath: .spec.replicas
      statusReplicasPath: .status.replicas
      labelSelectorPath: .status.selector
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
              selector:
                type: object
          status:
            type: object
            properties:
              replicas:
                type: integer
              readyReplicas:
                type: integer
              selector:
                type: string
              conditions:
                type: array
                items:
                  type: object
```

## Implement Proper Status Updates

Your controller should update the status with pod information:

```go
// Update status with current pod count and selector
status := &BoundedDeploymentStatus{
    Replicas:      int32(len(allPods)),
    ReadyReplicas: int32(len(readyPods)),
    Selector:      labels.Set(boundedDeployment.Spec.Selector.MatchLabels).String(),
}
boundedDeployment.Status = *status
```

## Use consistent labels

Ensure your pods have consistent labels that match your selector:

```yaml
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: my-app
    boundeddeployment: my-boundeddeployment
spec:
  # ... pod spec
```
