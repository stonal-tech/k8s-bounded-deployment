package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"

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
}

func (r *MinDeploymentReconciler) init() {
	r.Log = slog.Default()
	r.deploymentToMinDeployment = make(map[types.NamespacedName]types.NamespacedName)
}

// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=mindeployments/finalizers,verbs=update

func (r *MinDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With("namespace", req.Namespace, "name", req.Name)
	log.Info("Reconciling MinDeployment")

	minDeployment, err := r.getMinDeployment(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	templateHash, err := r.handleSourceDeployment(ctx, minDeployment, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := minDeployment.Check(); err != nil {
		log.Error("Invalid MinDeployment", "error", err)
		return ctrl.Result{}, err
	}

	activePods, err := r.getActivePods(ctx, minDeployment, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcilePods(ctx, minDeployment, activePods, templateHash); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(ctx, minDeployment); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *MinDeploymentReconciler) getMinDeployment(
	ctx context.Context,
	namespacedName types.NamespacedName,
) (*v1.MinDeployment, error) {
	minDeployment := &v1.MinDeployment{}
	if err := r.Get(ctx, namespacedName, minDeployment); err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("MinDeployment not found. Ignoring since it must have been deleted")
			return nil, nil
		}
		r.Log.Error("Failed to get MinDeployment", "error", err)
		return nil, err
	}
	return minDeployment, nil
}

func (r *MinDeploymentReconciler) handleSourceDeployment(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	namespace string,
) (string, error) {
	if minDeployment.Spec.SourceDeploymentName == "" {
		return generatePodTemplateHash(minDeployment.Spec.Template), nil
	}

	deployment := &appsv1.Deployment{}
	if err := r.Get(
		ctx,
		client.ObjectKey{Name: minDeployment.Spec.SourceDeploymentName, Namespace: namespace},
		deployment); err != nil {
		r.Log.Error("Failed to get Deployment", "sourceDeploymentName", minDeployment.Spec.SourceDeploymentName, "err", err)
		return "", err
	}

	r.deploymentToMinDeployment[types.NamespacedName{Namespace: namespace, Name: deployment.Name}] = types.NamespacedName{
		Namespace: namespace,
		Name:      minDeployment.Name,
	}

	minDeployment.Spec.Template = deployment.Spec.Template
	templateHash := generatePodTemplateHash(minDeployment.Spec.Template)

	if err := r.scaleDownDeployment(ctx, deployment); err != nil {
		return "", err
	}

	return templateHash, nil
}

func (r *MinDeploymentReconciler) scaleDownDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	if *deployment.Spec.Replicas != 0 {
		deployment.Spec.Replicas = new(int32)
		r.Log.Info("Disabling source Deployment", "sourceDeploymentName", deployment.Name)
		if err := r.Update(ctx, deployment); err != nil {
			r.Log.Error("Failed to scale down Deployment", "err", err)
			return err
		}
	}
	return nil
}

func (r *MinDeploymentReconciler) getActivePods(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	namespace string,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(minDeployment.Spec.Template.Labels),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		r.Log.Error("Failed to list pods", "err", err)
		return nil, err
	}

	activePods := make([]corev1.Pod, 0, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			activePods = append(activePods, pod)
		}
	}

	sort.Slice(activePods, func(i, j int) bool {
		return activePods[j].CreationTimestamp.Before(&activePods[i].CreationTimestamp)
	})

	return activePods, nil
}

func (r *MinDeploymentReconciler) reconcilePods(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	activePods []corev1.Pod,
	templateHash string,
) error {
	if err := r.deleteMismatchedPods(ctx, minDeployment, activePods, templateHash); err != nil {
		return err
	}

	podCount := len(activePods)
	minDeployment.Status.Replicas = podCount

	maxReplicas := r.calculateMaxReplicas(minDeployment)

	if podCount < minDeployment.Spec.Replicas {
		return r.createPod(ctx, minDeployment, templateHash)
	} else if maxReplicas != nil && podCount > *maxReplicas {
		return r.deletePod(ctx, minDeployment, &activePods[0])
	}

	r.Log.Debug("No action required")
	return nil
}

func (r *MinDeploymentReconciler) deleteMismatchedPods(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	activePods []corev1.Pod,
	templateHash string,
) error {
	for _, pod := range activePods {
		if pod.Annotations[PodTemplateHashAnnotation] != templateHash {
			r.Log.Info(
				"Deleting pod due to hash mismatch",
				"pod", pod.Name,
				"podHash", pod.Annotations[PodTemplateHashAnnotation],
			)
			if err := r.Delete(ctx, &pod); err != nil {
				r.Log.Error("Failed to delete pod", "err", err)
				return err
			}
			minDeployment.Status.NbPodsDeleted++
		}
	}
	return nil
}

func (r *MinDeploymentReconciler) calculateMaxReplicas(minDeployment *v1.MinDeployment) *int {
	if minDeployment.Spec.MaxReplicas != nil {
		return minDeployment.Spec.MaxReplicas
	}
	if minDeployment.Spec.MarginReplicas != nil {
		maxReplicas := minDeployment.Spec.Replicas + *minDeployment.Spec.MarginReplicas
		return &maxReplicas
	}
	return nil
}

func (r *MinDeploymentReconciler) createPod(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	templateHash string,
) error {
	r.Log.Info(
		"Creating pod",
		"minReplicas", minDeployment.Spec.Replicas,
		"currentReplicas", minDeployment.Status.Replicas,
	)
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: minDeployment.Name + "-pod-",
			Namespace:    minDeployment.Namespace,
			Labels:       minDeployment.Spec.Template.Labels,
			Annotations: map[string]string{
				PodTemplateHashAnnotation: templateHash,
			},
		},
		Spec: minDeployment.Spec.Template.Spec,
	}

	if err := controllerutil.SetControllerReference(minDeployment, newPod, r.Scheme); err != nil {
		r.Log.Error("Failed to set controller reference", "err", err)
		return err
	}

	if err := r.Create(ctx, newPod); err != nil {
		r.Log.Error("Failed to create new pod", "err", err)
		return err
	}
	minDeployment.Status.NbPodsCreated++
	return nil
}

func (r *MinDeploymentReconciler) deletePod(
	ctx context.Context,
	minDeployment *v1.MinDeployment,
	pod *corev1.Pod,
) error {
	r.Log.Info("Deleting a pod", "maxReplicas", *r.calculateMaxReplicas(minDeployment))
	if err := r.Delete(ctx, pod); err != nil {
		r.Log.Error("Failed to delete pod", "err", err)
		return err
	}
	minDeployment.Status.NbPodsDeleted++
	return nil
}

func (r *MinDeploymentReconciler) updateStatus(ctx context.Context, minDeployment *v1.MinDeployment) error {
	if err := r.Status().Update(ctx, minDeployment); err != nil {
		r.Log.Error("Failed to update MinDeployment status", "err", err)
		return err
	}
	return nil
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
