package controller

import (
	"context"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "sigs.k8s.io/controller-runtime/pkg/client"
)

type reconcileState struct {
	coreSets      []*appsv1.StatefulSet
	replicantSets []*appsv1.ReplicaSet
	pods          []*corev1.Pod
}

func (r *reconcileState) podWithName(name string) *corev1.Pod {
	for _, pod := range r.pods {
		if pod.Name == name {
			return pod
		}
	}
	return nil
}

func (r *reconcileState) podsWithRole(role string) []*corev1.Pod {
	var list []*corev1.Pod
	for _, pod := range r.pods {
		if pod.Labels[appsv2beta1.LabelsDBRoleKey] == role {
			list = append(list, pod)
		}
	}
	return list
}

func (r *reconcileState) podsManagedBy(uid types.UID) []*corev1.Pod {
	var list []*corev1.Pod
	for _, pod := range r.pods {
		if metav1.GetControllerOf(pod) != nil && metav1.GetControllerOf(pod).UID == uid {
			list = append(list, pod)
		}
	}
	return list
}

func (r *reconcileState) currentCoreSet(instance *appsv2beta1.EMQX) *appsv1.StatefulSet {
	for _, sts := range r.coreSets {
		hash := sts.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		if hash == instance.Status.CoreNodesStatus.CurrentRevision {
			return sts
		}
	}
	return nil
}

func (r *reconcileState) currentReplicantSet(instance *appsv2beta1.EMQX) *appsv1.ReplicaSet {
	for _, rs := range r.replicantSets {
		hash := rs.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		if hash == instance.Status.ReplicantNodesStatus.CurrentRevision {
			return rs
		}
	}
	return nil
}

func (r *reconcileState) updateCoreSet(instance *appsv2beta1.EMQX) *appsv1.StatefulSet {
	for _, sts := range r.coreSets {
		hash := sts.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		if hash == instance.Status.CoreNodesStatus.UpdateRevision {
			return sts
		}
	}
	return nil
}

func (r *reconcileState) updateReplicantSet(instance *appsv2beta1.EMQX) *appsv1.ReplicaSet {
	for _, rs := range r.replicantSets {
		hash := rs.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		if hash == instance.Status.ReplicantNodesStatus.UpdateRevision {
			return rs
		}
	}
	return nil
}

func (r *reconcileState) partOfCurrentSet(pod *corev1.Pod, instance *appsv2beta1.EMQX) bool {
	controllerRef := metav1.GetControllerOf(pod)
	if controllerRef == nil {
		return false
	}
	currentCoreSet := r.currentCoreSet(instance)
	if currentCoreSet != nil && controllerRef.UID == currentCoreSet.UID {
		return true
	}
	currentReplicantSet := r.currentReplicantSet(instance)
	if currentReplicantSet != nil && controllerRef.UID == currentReplicantSet.UID {
		return true
	}
	return false
}

func (r *reconcileState) partOfUpdateSet(pod *corev1.Pod, instance *appsv2beta1.EMQX) bool {
	controllerRef := metav1.GetControllerOf(pod)
	if controllerRef == nil {
		return false
	}
	updateCoreSet := r.updateCoreSet(instance)
	if updateCoreSet != nil && controllerRef.UID == updateCoreSet.UID {
		return true
	}
	updateReplicantSet := r.updateReplicantSet(instance)
	if updateReplicantSet != nil && controllerRef.UID == updateReplicantSet.UID {
		return true
	}
	return false
}

type loadState struct {
	*EMQXReconciler
}

func (l *loadState) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	state := loadReconcileState(r.ctx, l.Client, instance)
	r.state = state
	return subResult{}
}

func loadReconcileState(ctx context.Context, client k8s.Client, instance *appsv2beta1.EMQX) *reconcileState {
	state := &reconcileState{}

	stsList := &appsv1.StatefulSetList{}
	_ = client.List(ctx, stsList,
		k8s.InNamespace(instance.Namespace),
		k8s.MatchingLabels(appsv2beta1.DefaultCoreLabels(instance)),
	)

	for _, sts := range stsList.Items {
		state.coreSets = append(state.coreSets, sts.DeepCopy())
	}

	sortByCreationTimestamp(state.coreSets)

	rsList := &appsv1.ReplicaSetList{}
	_ = client.List(ctx, rsList,
		k8s.InNamespace(instance.Namespace),
		k8s.MatchingLabels(appsv2beta1.DefaultReplicantLabels(instance)),
	)

	for _, rs := range rsList.Items {
		state.replicantSets = append(state.replicantSets, rs.DeepCopy())
	}

	sortByCreationTimestamp(state.replicantSets)

	podList := &corev1.PodList{}
	_ = client.List(ctx, podList,
		k8s.InNamespace(instance.Namespace),
		k8s.MatchingLabels(appsv2beta1.DefaultLabels(instance)),
	)

	for _, pod := range podList.Items {
		// Disregard pods that are being deleted.
		if pod.GetDeletionTimestamp() != nil {
			continue
		}

		// Disregard pods that are not controlled by any controller.
		controllerRef := metav1.GetControllerOf(&pod)
		if controllerRef == nil {
			continue
		}

		// Add the pod to the list of pods.
		pod := pod.DeepCopy()
		state.pods = append(state.pods, pod)
	}

	return state
}
