package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	deploymentv1 "github.com/stonal-tech/tool-k8s-crd-mindeployment/api/v1"
	v1 "github.com/stonal-tech/tool-k8s-crd-mindeployment/api/v1"
)

const PodTemplateHashAnnotation = "deploy.stonal.io/template-hash"

// generatePodTemplateHash generates a hash for a given pod template spec.
func generatePodTemplateHash(template corev1.PodTemplateSpec) string {
	hasher := sha256.New()
	hasher.Write([]byte(template.Spec.String()))
	return hex.EncodeToString(hasher.Sum(nil))
}

// MinDeploymentReconciler reconciles a MinDeployment object
type MinDeploymentReconciler struct {
	client.Client
	Log    *slog.Logger
	Scheme *runtime.Scheme

	deploymentToMinDeployment map[types.NamespacedName]types.NamespacedName
	lock                      sync.Mutex
}

func (r *MinDeploymentReconciler) init() {
	r.Log = slog.Default()
	r.deploymentToMinDeployment = make(map[types.NamespacedName]types.NamespacedName)
}

// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/finalizers,verbs=update

func (r *MinDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	log := r.Log.With("namespace", req.Namespace, "name", req.Name)
	log.Info("Reconciling MinDeployment")

	// Fetch the MinDeployment instance
	minDeployment := &v1.MinDeployment{}
	errGet := r.Get(ctx, req.NamespacedName, minDeployment)
	if errGet != nil {
		if errors.IsNotFound(errGet) {
			log.Info("MinDeployment not found. Ignoring since it must have been deleted")
			return ctrl.Result{}, nil
		}
		r.Log.Error("Failed to get MinDeployment", "error", errGet)
		return ctrl.Result{}, errGet
	}

	// Generate a hash for the current pod template
	templateHash := generatePodTemplateHash(minDeployment.Spec.Template)
	log = log.With("templateHash", templateHash)

	// Fetch the referenced Deployment if needed
	if minDeployment.Spec.SourceDeploymentName != "" {
		deployment := &appsv1.Deployment{}
		errGet = r.Get(
			ctx,
			client.ObjectKey{
				Name:      minDeployment.Spec.SourceDeploymentName,
				Namespace: req.Namespace,
			},
			deployment,
		)
		if errGet != nil {
			log.Error("Failed to get Deployment", "sourceDeploymentName", minDeployment.Spec.SourceDeploymentName, "err", errGet)
			return ctrl.Result{}, errGet
		}

		r.deploymentToMinDeployment[types.NamespacedName{Namespace: req.Namespace, Name: deployment.Name}] = req.NamespacedName

		// Copy the template from the Deployment and generate a new hash
		minDeployment.Spec.Template = deployment.Spec.Template
		templateHash = generatePodTemplateHash(minDeployment.Spec.Template)

		// Scale down the Deployment to disable it
		if *deployment.Spec.Replicas != 0 {
			deployment.Spec.Replicas = new(int32)
			log.Info("Disabling source Deployment", "sourceDeploymentName", minDeployment.Spec.SourceDeploymentName)
			if err := r.Update(ctx, deployment); err != nil {
				r.Log.Error("Failed to scale down Deployment", "err", err)
				return ctrl.Result{}, err
			}
		}
	}

	// Check if the MinDeployment is valid
	if err := minDeployment.Check(); err != nil {
		log.Error("Invalid MinDeployment", "error", err)
		return ctrl.Result{}, err
	}

	// Get the current list of pods
	podList := &corev1.PodList{}
	errGet = r.List(
		ctx,
		podList,
		client.InNamespace(req.Namespace),
		client.MatchingLabels(minDeployment.Spec.Template.Labels),
	)
	if errGet != nil {
		log.Error("Failed to list pods", "err", errGet)
		return ctrl.Result{}, errGet
	}

	// Remove the pods that have been marked for deletion
	activePods := make([]corev1.Pod, 0, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			activePods = append(activePods, pod)
		}
	}

	podCount := len(activePods)
	log = log.With("podCount", podCount)
	log.Info("Counted pod")

	minDeployment.Status.Replicas = podCount

	// Delete pods that do not match the current template hash
	for _, pod := range activePods {
		if pod.Annotations[PodTemplateHashAnnotation] != templateHash {
			log.Info("Deleting pod due to hash mismatch", "pod", pod.Name, "podHash", pod.Annotations[PodTemplateHashAnnotation])
			if err := r.Delete(ctx, &pod); err != nil {
				log.Error("Failed to delete pod", "err", err)
				return ctrl.Result{}, err
			}
			minDeployment.Status.NbPodsDeleted++
		}
	}

	minReplicas := minDeployment.Spec.Replicas
	maxReplicas := minDeployment.Spec.MaxReplicas

	if maxReplicas == nil && minDeployment.Spec.MarginReplicas != nil {
		maxReplicas = new(int)
		*maxReplicas = minReplicas + *minDeployment.Spec.MarginReplicas
	}

	if podCount < minReplicas {
		log.Info("Creating pod", "minReplicas", minDeployment.Spec.Replicas, "currentReplicas", podCount)
		// Create a new Pod
		newPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: minDeployment.Name + "-pod-",
				Namespace:    req.Namespace,
				Labels:       minDeployment.Spec.Template.Labels,
				Annotations: map[string]string{
					PodTemplateHashAnnotation: templateHash,
				},
			},
			Spec: minDeployment.Spec.Template.Spec,
		}

		// Set OwnerReference to ensure the pod is controlled by the MinDeployment resource
		if err := controllerutil.SetControllerReference(minDeployment, newPod, r.Scheme); err != nil {
			log.Error("Failed to set controller reference", "err", err)
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, newPod); err != nil {
			log.Error("Failed to create new pod", "err", err)
			return ctrl.Result{}, err
		}
		minDeployment.Status.NbPodsCreated++
	} else if maxReplicas != nil && podCount > *maxReplicas {
		log.Info("Deleting a pod", "maxReplicas", *maxReplicas)
		// Delete an existing Pod
		podToDelete := &activePods[0]
		if err := r.Delete(ctx, podToDelete); err != nil {
			log.Error("Failed to delete pod", "err", err)
			return ctrl.Result{}, err
		}
		minDeployment.Status.NbPodsDeleted++
	} else {
		log.Debug("No action required")
		return ctrl.Result{}, nil
	}

	if err := r.Status().Update(ctx, minDeployment); err != nil {
		log.Error("Failed to update MinDeployment status", "err", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *MinDeploymentReconciler) findDeployment(ctx context.Context, deployment client.Object) []reconcile.Request {
	deploymentsList := &appsv1.DeploymentList{}
	listOps := &client.ListOptions{
		// FieldSelector: fields.OneTermEqualSelector("configMapField", deployment.GetName()),
		Namespace: deployment.GetNamespace(),
	}
	err := r.List(ctx, deploymentsList, listOps)
	if err != nil {
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0, len(deploymentsList.Items))
	for _, item := range deploymentsList.Items {
		srcNsName := types.NamespacedName{Namespace: item.GetNamespace(), Name: item.GetName()}
		if targetNsName, ok := r.deploymentToMinDeployment[srcNsName]; ok {
			requests = append(
				requests,
				reconcile.Request{NamespacedName: targetNsName},
			)
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *MinDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.init()
	return ctrl.NewControllerManagedBy(mgr).
		For(&deploymentv1.MinDeployment{}).
		Owns(&corev1.Pod{}).
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.findDeployment),
		).
		Complete(r)
}
