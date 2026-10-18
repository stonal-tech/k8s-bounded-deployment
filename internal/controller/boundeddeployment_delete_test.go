package controller

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/stonal-tech/k8s-bounded-deployment/api/v1"
)

// testNamespace is the namespace these tests operate in.
const testNamespace = "default"

// TestDeletePodWithoutCeiling covers the case where neither spec.maxReplicas nor
// spec.margin is set, so there is no ceiling at all. Reaping a finished pod must still
// work: this is the ordinary path for a worker that exits 0.
func TestDeletePodWithoutCeiling(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register client-go scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register BoundedDeployment scheme: %v", err)
	}

	pod := &corev1.Pod{
		Name:      "worker-bounded-abc",
		Namespace: testNamespace,
		Status:    corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	boundedDep := &v1.BoundedDeployment{
		Name:      "worker",
		Namespace: testNamespace,
		Spec:      v1.BoundedDeploymentSpec{Replicas: 1},
	}

	reconciler := &BoundedDeploymentReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		Scheme: scheme,
	}

	if err := reconciler.deletePod(context.Background(), boundedDep, pod, slog.Default()); err != nil {
		t.Fatalf("deletePod returned an error: %v", err)
	}

	remaining := &corev1.Pod{}
	err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(pod), remaining)
	if err == nil {
		t.Fatal("expected the pod to be deleted, but it is still present")
	}
}
