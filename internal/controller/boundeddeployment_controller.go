package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/stonal-tech/k8s-bounded-deployment/api/v1"
)

const PodTemplateHashAnnotation = "deploy.stonal.io/template-hash"
const BoundedDeploymentLabel = "deploy.stonal.io/boundeddeployment"

// generatePodTemplateHash generates a hash for a given pod template spec.
func generatePodTemplateHash(template corev1.PodTemplateSpec) string {
	hasher := sha256.New()
	hasher.Write([]byte(template.Spec.String()))
	return hex.EncodeToString(hasher.Sum(nil))
}

// BoundedDeploymentReconciler reconciles a BoundedDeployment object
type BoundedDeploymentReconciler struct {
	client.Client
	Log    *slog.Logger
	Scheme *runtime.Scheme
}

func (r *BoundedDeploymentReconciler) init() {
	r.Log = slog.Default()
}

var ErrRestartPolicyAlways = errors.New("RestartPolicy is Always, patching it to OnFailure")

func (r *BoundedDeploymentReconciler) checkBoundedDeployment(
	ctx context.Context,
	boundedDep *v1.BoundedDeployment,
	log *slog.Logger,
) error {
	if err := boundedDep.Check(); err != nil {
		log.Error("Invalid BoundedDeployment", "error", err)
		return err
	}

	if boundedDep.Spec.Template.Spec.RestartPolicy == corev1.RestartPolicyAlways {
		log.Warn("RestartPolicy is Always, patching it to OnFailure")
		boundedDep.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure

		if err := r.Update(ctx, boundedDep); err != nil {
			log.Error("Failed to patch RestartPolicy", "error", err)
			return err
		}

		return ErrRestartPolicyAlways
	}

	return nil
}

func countNbReadyReplicas(pods []corev1.Pod) int {
	readyReplicas := 0
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					readyReplicas++
					break
				}
			}
		}
	}
	return readyReplicas
}

// +kubebuilder:rbac:groups=deploy.stonal.io,resources=boundeddeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=boundeddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=deploy.stonal.io,resources=boundeddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *BoundedDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.Log == nil {
		r.init()
	}
	log := r.Log.With("namespace", req.Namespace, "name", req.Name)
	log.Debug("Reconciling BoundedDeployment")

	boundedDep, err := r.getBoundedDeployment(ctx, req.NamespacedName, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	boundedBefore := boundedDep.DeepCopy()

	if err := r.checkBoundedDeployment(ctx, boundedDep, log); err != nil {
		return ctrl.Result{}, err
	}

	templateHash := generatePodTemplateHash(boundedDep.Spec.Template)

	activePods, err := r.getActivePods(ctx, boundedDep, req.Namespace, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcilePods(ctx, boundedDep, activePods, templateHash, log); err != nil {
		return ctrl.Result{}, err
	}

	boundedDep.Status.Replicas = len(activePods)
	boundedDep.Status.ReadyReplicas = countNbReadyReplicas(activePods)
	boundedDep.Status.TemplateHash = templateHash
	boundedDep.Status.Selector = getLabelsAsSelector(boundedDep)

	if boundedBefore.Status != boundedDep.Status {
		log.Info("Status updated", "status", boundedDep.Status)

		if err := r.updateStatus(ctx, boundedBefore, boundedDep, log); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *BoundedDeploymentReconciler) getBoundedDeployment(
	ctx context.Context,
	namespacedName types.NamespacedName,
	log *slog.Logger,
) (*v1.BoundedDeployment, error) {
	boundeDeployment := &v1.BoundedDeployment{}
	if err := r.Get(ctx, namespacedName, boundeDeployment); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Info("BoundedDeployment not found. Ignoring since it must have been deleted")
			return nil, nil
		}
		log.Error("Failed to get BoundedDeployment", "error", err)
		return nil, err
	}

	return boundeDeployment, nil
}

func (r *BoundedDeploymentReconciler) getActivePods(
	ctx context.Context,
	boundedDeployment *v1.BoundedDeployment,
	namespace string,
	log *slog.Logger,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			BoundedDeploymentLabel: boundedDeployment.Name,
		}),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		log.Error("Failed to list pods", "err", err)
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

func (r *BoundedDeploymentReconciler) reconcilePods(
	ctx context.Context,
	boundedDep *v1.BoundedDeployment,
	activePods []corev1.Pod,
	templateHash string,
	log *slog.Logger,
) error {
	// Delete pods with Succeeded status
	for i := range activePods {
		pod := &activePods[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			log.Info(
				"Deleting pod",
				"podName", pod.Name,
				"podStatusPhase", pod.Status.Phase,
				"podStatusReason", pod.Status.Reason,
			)
			if err := r.deletePod(ctx, boundedDep, pod, log); err != nil {
				return fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
			}
			// Pod will be deleted, continue with reconciliation
			return nil
		}
	}

	if err := r.deleteMismatchedPods(ctx, activePods, templateHash, log); err != nil {
		return err
	}

	podCount := len(activePods)

	maxReplicas := r.calculateMaxReplicas(boundedDep)

	if podCount < boundedDep.Spec.Replicas {
		return r.createPod(ctx, boundedDep, templateHash, log)
	} else if maxReplicas != nil && podCount > *maxReplicas {
		return r.deletePod(ctx, boundedDep, &activePods[0], log)
	}

	log.Debug("No action required")
	return nil
}

func (r *BoundedDeploymentReconciler) deleteMismatchedPods(
	ctx context.Context,
	activePods []corev1.Pod,
	templateHash string,
	log *slog.Logger,
) error {
	for _, pod := range activePods {
		podHash := pod.Annotations[PodTemplateHashAnnotation]
		if podHash == "" {
			log.Warn("Pod has no hash annotation, this probably means it's not a BoundedDeployment pod")
			continue
		}
		if podHash != templateHash {
			log.Info(
				"Deleting pod due to hash mismatch",
				"pod", pod.Name,
				"podHash", podHash,
			)
			if err := r.Delete(ctx, &pod); err != nil {
				log.Error("Failed to delete pod", "err", err)
				return err
			}
		}
	}
	return nil
}

func (r *BoundedDeploymentReconciler) calculateMaxReplicas(boundedDep *v1.BoundedDeployment) *int {
	if boundedDep.Spec.MaxReplicas != nil {
		return boundedDep.Spec.MaxReplicas
	}
	if boundedDep.Spec.MarginReplicas != nil {
		maxReplicas := boundedDep.Spec.Replicas + *boundedDep.Spec.MarginReplicas
		return &maxReplicas
	}
	return nil
}

func (r *BoundedDeploymentReconciler) createPod(
	ctx context.Context,
	boundedDep *v1.BoundedDeployment,
	templateHash string,
	log *slog.Logger,
) error {
	log.Info(
		"Creating pod",
		"minReplicas", boundedDep.Spec.Replicas,
		"currentReplicas", boundedDep.Status.Replicas,
	)

	// Create a copy of labels from the template
	labels := make(map[string]string)
	for k, v := range boundedDep.Spec.Template.Labels {
		labels[k] = v
	}

	// Add the BoundedDeployment label
	labels[BoundedDeploymentLabel] = boundedDep.Name

	// Create a copy of annotations from the template
	annotations := make(map[string]string)
	for k, v := range boundedDep.Spec.Template.Annotations {
		annotations[k] = v
	}

	// Add the template hash annotation
	annotations[PodTemplateHashAnnotation] = templateHash

	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: boundedDep.Name + "-bounded-",
			Namespace:    boundedDep.Namespace,
			Labels:       labels,
			Annotations:  annotations,
		},
		Spec: boundedDep.Spec.Template.Spec,
	}

	if err := controllerutil.SetControllerReference(boundedDep, newPod, r.Scheme); err != nil {
		log.Error("Failed to set controller reference", "err", err)
		return err
	}

	if err := r.Create(ctx, newPod); err != nil {
		log.Error("Failed to create new pod", "err", err)
		return err
	}
	return nil
}

func (r *BoundedDeploymentReconciler) deletePod(
	ctx context.Context,
	minDeployment *v1.BoundedDeployment,
	pod *corev1.Pod,
	log *slog.Logger,
) error {
	log.Info("Deleting a pod", "maxReplicas", *r.calculateMaxReplicas(minDeployment))
	if err := r.Delete(ctx, pod); err != nil {
		log.Error("Failed to delete pod", "err", err)
		return err
	}
	return nil
}

func (r *BoundedDeploymentReconciler) updateStatus(
	ctx context.Context,
	boundedBefore *v1.BoundedDeployment,
	boundedDep *v1.BoundedDeployment,
	log *slog.Logger,
) error {
	statusPatch := client.MergeFrom(boundedBefore)
	if err := r.Status().Patch(ctx, boundedDep, statusPatch); err != nil {
		if k8serrors.IsConflict(err) {
			log.Debug("Conflict detected when updating status", "err", err)
			return nil
		}

		log.Error("Failed to patch BoundedDeployment status", "err", err)
		return err
	}

	return nil
}

// getLabelsAsSelector returns the label selector for pods managed by this BoundedDeployment
func getLabelsAsSelector(boundedDep *v1.BoundedDeployment) string {
	return labels.Set(map[string]string{
		BoundedDeploymentLabel: boundedDep.Name,
	}).String()
}

// SetupWithManager sets up the controller with the Manager.
func (r *BoundedDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.init()
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.BoundedDeployment{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
