package controllers

import (
	"context"
	"log/slog"

	v1 "github.com/stonal-tech/tool-k8s-crd-mindeployment/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MinDeploymentReconciler struct {
	client.Client
	Log    *slog.Logger
	Scheme *runtime.Scheme
}

func (r *MinDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("Reconciling MinDeployment", "namespace", req.Namespace, "name", req.Name)

	// Fetch the MinDeployment instance
	minDeployment := &v1.MinDeployment{}
	err := r.Get(ctx, req.NamespacedName, minDeployment)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("MinDeployment not found. Ignoring since it must have been deleted", "namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		r.Log.Error("Failed to get MinDeployment", "error", err)
		return ctrl.Result{}, err
	}

	// Get the current list of pods
	podList := &corev1.PodList{}
	err = r.List(ctx, podList, client.InNamespace(req.Namespace), client.MatchingLabels(minDeployment.Spec.Template.Labels))
	if err != nil {
		r.Log.Error("Failed to list pods", "namespace", req.Namespace, "name", req.Name, "error", err)
		return ctrl.Result{}, err
	}

	podCount := len(podList.Items)
	r.Log.Info("Current pod count", "namespace", req.Namespace, "name", req.Name, "count", podCount)

	if podCount < minDeployment.Spec.Replicas {
		r.Log.Info("Pod count below desired replicas, creating new pod", "namespace", req.Namespace, "name", req.Name)
		// Create a new Pod
		newPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: minDeployment.Name + "-pod-",
				Namespace:    req.Namespace,
				Labels:       minDeployment.Spec.Template.Labels,
			},
			Spec: minDeployment.Spec.Template.Spec,
		}
		if err := r.Create(ctx, newPod); err != nil {
			r.Log.Error("Failed to create new pod", "namespace", req.Namespace, "name", req.Name, "error", err)
			return ctrl.Result{}, err
		}
	} else if minDeployment.Spec.MaxReplicas != nil && podCount > *minDeployment.Spec.MaxReplicas {
		r.Log.Info("Pod count above max replicas, deleting a pod", "namespace", req.Namespace, "name", req.Name)
		// Delete an existing Pod
		podToDelete := podList.Items[0]
		if err := r.Delete(ctx, &podToDelete); err != nil {
			r.Log.Error("Failed to delete pod", "namespace", req.Namespace, "name", req.Name, "error", err)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *MinDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MinDeployment{}).
		Complete(r)
}
