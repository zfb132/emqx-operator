package controller

import (
	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	corev1 "k8s.io/api/core/v1"
)

type updatePodConditions struct {
	*EMQXReconciler
}

func (u *updatePodConditions) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	for _, pod := range r.state.pods {
		onServingCondition := appsv2beta1.FindPodCondition(pod, appsv2beta1.PodOnServing)
		if onServingCondition == nil {
			onServingCondition = &corev1.PodCondition{
				Type:   appsv2beta1.PodOnServing,
				Status: corev1.ConditionUnknown,
			}
		}

		if r.state.partOfUpdateSet(pod, instance) {
			cond := appsv2beta1.FindPodCondition(pod, corev1.ContainersReady)
			if cond != nil && cond.Status == corev1.ConditionTrue {
				status := u.checkRebalanceStatus(r, pod)
				appsv2beta1.ChangeCondition(onServingCondition, status)
			}
		} else {
			if r.state.partOfCurrentSet(pod, instance) {
				// When available condition is true, need clean currentSts / currentRs pod
				if instance.Status.IsConditionTrue(appsv2beta1.Available) {
					cond := appsv2beta1.FindPodCondition(pod, corev1.ContainersReady)
					if cond != nil && cond.Status == corev1.ConditionTrue {
						appsv2beta1.ChangeCondition(onServingCondition, corev1.ConditionFalse)
					}
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
