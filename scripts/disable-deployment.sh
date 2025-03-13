#!/bin/sh -ex

kubectl scale deployment -n k8s-bounded-deployment-system k8s-bounded-deployment-controller-manager --replicas=0

# wait for enter
read

kubectl scale deployment -n k8s-bounded-deployment-system k8s-bounded-deployment-controller-manager --replicas=1
