package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
func generatePodTemplateHash(template *corev1.PodTemplateSpec) string {
	hasher := sha256.New()
	hasher.Write([]byte(template.Spec.String()))
	return hex.EncodeToString(hasher.Sum(nil))
}

// BoundedDeploymentReconciler reconciles a BoundedDeployment object.
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
		log.ErrorContext(ctx, "Invalid BoundedDeployment", "error", err)
		return err
	}

	if boundedDep.Spec.Template.Spec.RestartPolicy == corev1.RestartPolicyAlways {
		log.WarnContext(ctx, "RestartPolicy is Always, patching it to OnFailure")
		boundedDep.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure

		if err := r.Update(ctx, boundedDep); err != nil {
			log.ErrorContext(ctx, "Failed to patch RestartPolicy", "error", err)
			return err
		}

		return ErrRestartPolicyAlways
	}

	return nil
}

func countNbReadyReplicas(pods []corev1.Pod) int {
	readyReplicas := 0
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for j := range pod.Status.Conditions {
			condition := &pod.Status.Conditions[j]
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				readyReplicas++
				break
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
	log := r.Log.With("namespace", req.Namespace, "name", req.Name)
	log.DebugContext(ctx, "Reconciling BoundedDeployment")

	boundedDep, err := r.getBoundedDeployment(ctx, req.NamespacedName, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	boundedBefore := boundedDep.DeepCopy()

	if checkErr := r.checkBoundedDeployment(ctx, boundedDep, log); checkErr != nil {
		return ctrl.Result{}, checkErr
	}

	templateHash := generatePodTemplateHash(&boundedDep.Spec.Template)

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

	if boundedBefore.Status != boundedDep.Status {
		log.InfoContext(ctx, "Status updated", "status", boundedDep.Status)

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
			log.InfoContext(ctx, "BoundedDeployment not found. Ignoring since it must have been deleted")
			return nil, nil
		}
		log.ErrorContext(ctx, "Failed to get BoundedDeployment", "error", err)
		return nil, err
	}

	return boundeDeployment, nil
}

func (r *BoundedDeploymentReconciler) getActivePods(
	ctx context.Context,
	minDeployment *v1.BoundedDeployment,
	namespace string,
	log *slog.Logger,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			BoundedDeploymentLabel: minDeployment.Name,
		}),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		log.ErrorContext(ctx, "Failed to list pods", "err", err)
		return nil, err
	}

	activePods := make([]corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		if podList.Items[i].DeletionTimestamp == nil {
			activePods = append(activePods, podList.Items[i])
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
			log.InfoContext(ctx,
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

	log.DebugContext(ctx, "No action required")
	return nil
}

func (r *BoundedDeploymentReconciler) deleteMismatchedPods(
	ctx context.Context,
	activePods []corev1.Pod,
	templateHash string,
	log *slog.Logger,
) error {
	for i := range activePods {
		pod := &activePods[i]
		podHash := pod.Annotations[PodTemplateHashAnnotation]
		if podHash == "" {
			log.WarnContext(ctx, "Pod has no hash annotation, this probably means it's not a BoundedDeployment pod")
			continue
		}
		if podHash != templateHash {
			log.InfoContext(ctx,
				"Deleting pod due to hash mismatch",
				"pod", pod.Name,
				"podHash", podHash,
			)
			if err := r.Delete(ctx, pod); err != nil {
				log.ErrorContext(ctx, "Failed to delete pod", "err", err)
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
	log.InfoContext(ctx,
		"Creating pod",
		"minReplicas", boundedDep.Spec.Replicas,
		"currentReplicas", boundedDep.Status.Replicas,
	)

	// Create a copy of labels from the template
	labels := make(map[string]string)
	maps.Copy(labels, boundedDep.Spec.Template.Labels)

	// Add the BoundedDeployment label
	labels[BoundedDeploymentLabel] = boundedDep.Name

	// Create a copy of annotations from the template
	annotations := make(map[string]string)
	maps.Copy(annotations, boundedDep.Spec.Template.Annotations)

	// Add the template hash annotation
	annotations[PodTemplateHashAnnotation] = templateHash

	newPod := &corev1.Pod{
		GenerateName: boundedDep.Name + "-bounded-",
		Namespace:    boundedDep.Namespace,
		Labels:       labels,
		Annotations:  annotations,
		Spec:         boundedDep.Spec.Template.Spec,
	}

	if err := controllerutil.SetControllerReference(boundedDep, newPod, r.Scheme); err != nil {
		log.ErrorContext(ctx, "Failed to set controller reference", "err", err)
		return err
	}

	if err := r.Create(ctx, newPod); err != nil {
		log.ErrorContext(ctx, "Failed to create new pod", "err", err)
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
	// maxReplicas is nil when neither spec.maxReplicas nor spec.margin is set, which is a
	// valid configuration: pods are then only ever removed by finishing.
	if maxReplicas := r.calculateMaxReplicas(minDeployment); maxReplicas != nil {
		log.InfoContext(ctx, "Deleting a pod", "podName", pod.Name, "maxReplicas", *maxReplicas)
	} else {
		log.InfoContext(ctx, "Deleting a pod", "podName", pod.Name)
	}

	if err := r.Delete(ctx, pod); err != nil {
		log.ErrorContext(ctx, "Failed to delete pod", "err", err)
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
			log.DebugContext(ctx, "Conflict detected when updating status", "err", err)
			return nil
		}

		log.ErrorContext(ctx, "Failed to patch BoundedDeployment status", "err", err)
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BoundedDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.init()
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.BoundedDeployment{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
