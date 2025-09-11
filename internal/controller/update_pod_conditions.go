package controller

import (
	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	corev1 "k8s.io/api/core/v1"
)

type updatePodConditions struct {
	*EMQXReconciler
}

func (u *updatePodConditions) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	for _, pod := range r.state.pods {
		onServingCondition := util.FindPodCondition(pod, appsv2beta1.PodOnServing)
		if onServingCondition == nil {
			onServingCondition = &corev1.PodCondition{
				Type:   appsv2beta1.PodOnServing,
				Status: corev1.ConditionUnknown,
			}
		}

		if r.state.partOfUpdateSet(pod, instance) {
			cond := util.FindPodCondition(pod, corev1.ContainersReady)
			if cond != nil && cond.Status == corev1.ConditionTrue {
				req := r.api.SwitchHost(pod.Status.PodIP)
				status := api.AvailabilityCheck(req)
				util.SwitchPodConditionStatus(onServingCondition, status)
			}
		} else {
			if r.state.partOfCurrentSet(pod, instance) {
				// When available condition is true, need clean currentSts / currentRs pod
				if instance.Status.IsConditionTrue(appsv2beta1.Available) {
					cond := util.FindPodCondition(pod, corev1.ContainersReady)
					if cond != nil && cond.Status == corev1.ConditionTrue {
						util.SwitchPodConditionStatus(onServingCondition, corev1.ConditionFalse)
					}
				}
			}
		}

		err := util.UpdatePodCondition(r.ctx, u.Client, pod, *onServingCondition)
		if err != nil {
			return subResult{err: emperror.Wrapf(err, "failed to update pod %s status", pod.Name)}
		}
	}
	return subResult{}
}
