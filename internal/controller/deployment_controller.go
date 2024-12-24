package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	deploymentv1 "github.com/stonal-tech/k8s-bounded-deployment/api/v1"
)

const (
	BoundedAnnotation = "stonal.io/bounded"
)

// DeploymentController reconciles Deployment objects
type DeploymentController struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *DeploymentController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the Deployment
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if deployment has our annotation
	if val, exists := deployment.Annotations[BoundedAnnotation]; !exists || val != "true" {
		return ctrl.Result{}, nil
	}

	// Create or update BoundedDeployment
	boundedDep := &deploymentv1.BoundedDeployment{}
	boundedDep.Name = fmt.Sprintf("%s-bounded", deployment.Name)
	boundedDep.Namespace = deployment.Namespace
	boundedDep.Spec.Template = deployment.Spec.Template
	boundedDep.Spec.Replicas = int(*deployment.Spec.Replicas)
	
	// Scale down the original deployment
	zero := int32(0)
	deployment.Spec.Replicas = &zero
	if err := r.Update(ctx, deployment); err != nil {
		logger.Error(err, "failed to scale down deployment")
		return ctrl.Result{}, err
	}

	// Create/Update the BoundedDeployment
	if err := r.Create(ctx, boundedDep); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			logger.Error(err, "failed to create BoundedDeployment")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *DeploymentController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Complete(r)
}
