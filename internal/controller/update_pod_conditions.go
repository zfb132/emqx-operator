package controller

import (
	"slices"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type updatePodConditions struct {
	*EMQXReconciler
}

func (u *updatePodConditions) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	pods := r.state.pods

	var updateControllerUids []types.UID
	var currentControllerUids []types.UID
	if r.state.updateSts != nil {
		updateControllerUids = append(updateControllerUids, r.state.updateSts.UID)
	}
	if r.state.updateRs != nil {
		updateControllerUids = append(updateControllerUids, r.state.updateRs.UID)
	}
	if r.state.currentSts != nil {
		currentControllerUids = append(currentControllerUids, r.state.currentSts.UID)
	}
	if r.state.currentRs != nil {
		currentControllerUids = append(currentControllerUids, r.state.currentRs.UID)
	}

	for _, pod := range pods {
		controllerRef := metav1.GetControllerOf(pod)
		controllerUid := controllerRef.UID

		if pod.DeletionTimestamp != nil {
			continue
		}

		onServingCondition := appsv2beta1.FindPodCondition(pod, appsv2beta1.PodOnServing)
		if onServingCondition == nil {
			onServingCondition = &corev1.PodCondition{
				Type:   appsv2beta1.PodOnServing,
				Status: corev1.ConditionUnknown,
			}
		}

		if slices.Contains(updateControllerUids, controllerUid) {
			containersReadyCondition := appsv2beta1.FindPodCondition(pod, corev1.ContainersReady)
			if containersReadyCondition != nil && containersReadyCondition.Status == corev1.ConditionTrue {
				status := u.checkInCluster(r, pod)
				appsv2beta1.ChangeCondition(onServingCondition, status)
			}
		}

		if slices.Contains(currentControllerUids, controllerUid) {
			// When available condition is true, need clean currentSts / currentRs pod
			if instance.Status.IsConditionTrue(appsv2beta1.Available) {
				containersReadyCondition := appsv2beta1.FindPodCondition(pod, corev1.ContainersReady)
				if containersReadyCondition != nil && containersReadyCondition.Status == corev1.ConditionTrue {
					appsv2beta1.ChangeCondition(onServingCondition, corev1.ConditionFalse)
				}
			}
		}

		err := updatePodCondition(r.ctx, u.Client, pod, *onServingCondition)
		if err != nil {
			return subResult{err: emperror.Wrapf(err, "failed to update pod %s status", pod.Name)}
		}
	}
	return subResult{}
}

func (u *updatePodConditions) checkInCluster(r *reconcileRound, pod *corev1.Pod) corev1.ConditionStatus {
	if r.state.corePods[pod.Name] == nil || r.state.replicantPods[pod.Name] == nil {
		return corev1.ConditionFalse
	}
	return u.checkRebalanceStatus(r, pod)
}

func (u *updatePodConditions) checkRebalanceStatus(r *reconcileRound, pod *corev1.Pod) corev1.ConditionStatus {
	req := r.api.SwitchHost(pod.Status.PodIP)
	url := req.GetURL("api/v5/load_rebalance/availability_check")
	resp, _, err := req.Request("GET", url, nil, nil)
	if err != nil {
		return corev1.ConditionUnknown
	}
	if resp.StatusCode != 200 {
		return corev1.ConditionFalse
	}
	return corev1.ConditionTrue
}
