#!/bin/sh -ex

kubectl patch boundeddeployment worker-ai-semantic-search -n dev --type merge -p '{"spec":{"replicas":10, "maxReplicas": 10}}'

# Watch the BoundedDeployment's status by exporting the .spec.replicas, .spec.maxReplicas, .status.nbPodsCreated, .status.nbPodsDeleted in the 
# same line
kubectl get boundeddeployment worker-ai-semantic-search -n dev -w -o jsonpath='SPEC: replicas:{.spec.replicas}, maxReplicas:{.spec.maxReplicas}{"  -  "}STATUS: replicas:{.status.replicas}, readyReplicas:{.status.readyReplicas}, templateHash:{.status.templateHash}{"\n\n"}'
