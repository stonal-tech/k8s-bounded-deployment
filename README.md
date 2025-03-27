https://chatgpt.com/share/5d132829-6ac8-4e09-b7ab-6c164c5c6649

## Listing active deployments
```sh
kubectl get boundeddeployments -A -w -o jsonpath='{.metadata.namespace}:{.metadata.name} - SPEC: replicas:{.spec.replicas}, maxReplicas:{.spec.maxReplicas}{"  -  "}STATUS: replicas:{.status.replicas}, readyReplicas:{.status.readyReplicas}{"\n"}'
```
