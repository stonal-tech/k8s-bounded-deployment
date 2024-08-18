/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	deploymentv1 "github.com/stonal-tech/tool-k8s-crd-mindeployment/api/v1"
	v1 "github.com/stonal-tech/tool-k8s-crd-mindeployment/api/v1"
)

// MinDeploymentReconciler reconciles a MinDeployment object
type MinDeploymentReconciler struct {
	client.Client
	Log    *slog.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MinDeployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *MinDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("Reconciling MinDeployment", "namespace", req.Namespace, "name", req.Name)

	// Fetch the MinDeployment instance
	minDeployment := &v1.MinDeployment{} // Using the correct path here
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

		// Set OwnerReference to ensure the pod is controlled by the MinDeployment resource
		if err := controllerutil.SetControllerReference(minDeployment, newPod, r.Scheme); err != nil {
			r.Log.Error("Failed to set controller reference", "namespace", req.Namespace, "name", req.Name, "error", err)
			return ctrl.Result{}, err
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

// SetupWithManager sets up the controller with the Manager.
func (r *MinDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Log = slog.Default()
	return ctrl.NewControllerManagedBy(mgr).
		For(&deploymentv1.MinDeployment{}).
		Complete(r)
}
