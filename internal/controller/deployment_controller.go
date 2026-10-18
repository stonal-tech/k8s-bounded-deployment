package controller

import (
	"context"
	"strconv"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	deploymentv1 "github.com/stonal-tech/k8s-bounded-deployment/api/v1"
)

const (
	BoundedAnnotationMargin  = "stonal.io/bounded-margin"
	BoundedAnnotationMax     = "stonal.io/bounded-max"
	BoundedAnnotationEnabled = "stonal.io/bounded-enabled"
)

// DeploymentController reconciles Deployment objects.
type DeploymentController struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments/finalizers,verbs=update

func (r *DeploymentController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the Deployment
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Create or update BoundedDeployment
	boundedDep := &deploymentv1.BoundedDeployment{}
	boundedDep.Name = deployment.Name
	boundedDep.Namespace = deployment.Namespace
	boundedDep.Spec.Template = deployment.Spec.Template
	boundedDep.Spec.Replicas = int(*deployment.Spec.Replicas)

	// The restartPolicy is left untouched, it will be handled by the BoundedDeployment controller

	enabled, err := applyBoundedAnnotations(deployment.Annotations, boundedDep, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !enabled {
		return ctrl.Result{}, nil
	}

	// Create/Update the BoundedDeployment
	if err := r.Create(ctx, boundedDep); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			logger.Error(err, "failed to create BoundedDeployment")
			return ctrl.Result{}, err
		}
	}

	// Scale down the original deployment
	zeroInt32 := int32(0)
	deployment.Spec.Replicas = &zeroInt32
	if err := r.Update(ctx, deployment); err != nil {
		logger.Error(err, "failed to scale down deployment")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// applyBoundedAnnotations reads the stonal.io/bounded-* annotations of a Deployment into the
// BoundedDeployment spec and reports whether bounding is enabled for that Deployment.
func applyBoundedAnnotations(
	annotations map[string]string,
	boundedDep *deploymentv1.BoundedDeployment,
	logger logr.Logger,
) (bool, error) {
	enabled := false

	if val, exists := annotations[BoundedAnnotationMax]; exists {
		maxReplicas, err := strconv.Atoi(val)
		if err != nil {
			logger.Error(err, "failed to convert max replicas to int")
			return false, err
		}
		boundedDep.Spec.MaxReplicas = &maxReplicas
		enabled = true
	}

	if val, exists := annotations[BoundedAnnotationMargin]; exists {
		marginReplicas, err := strconv.Atoi(val)
		if err != nil {
			logger.Error(err, "failed to convert margin replicas to int")
			return false, err
		}
		boundedDep.Spec.MarginReplicas = &marginReplicas
		enabled = true
	}

	if val, exists := annotations[BoundedAnnotationEnabled]; exists {
		parsed, err := strconv.ParseBool(val)
		if err != nil {
			logger.Error(err, "failed to convert enabled to bool")
			return false, err
		}
		enabled = parsed
	}

	return enabled, nil
}

func (r *DeploymentController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Complete(r)
}
