package controller

import (
	emperror "emperror.dev/errors"
	crdv2 "github.com/emqx/emqx-operator/api/v2"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	corev1 "k8s.io/api/core/v1"
)

type updatePodConditions struct {
	*EMQXReconciler
}

func (u *updatePodConditions) reconcile(r *reconcileRound, instance *crdv2.EMQX) subResult {
	for _, pod := range r.state.pods {
		onServingCondition := util.FindPodCondition(pod, crdv2.PodOnServing)
		if onServingCondition == nil {
			onServingCondition = &corev1.PodCondition{
				Type:   crdv2.PodOnServing,
				Status: corev1.ConditionUnknown,
			}
		}

		if r.state.partOfUpdateSet(pod, instance) {
			if util.IsPodConditionTrue(pod, corev1.ContainersReady) {
				status := api.AvailabilityCheck(r.requester.forPod(pod))
				util.SwitchPodConditionStatus(onServingCondition, status)
			}
		} else {
			if r.state.partOfCurrentSet(pod, instance) {
				// When available condition is true, need clean currentSts / currentRs pod
				if instance.Status.IsConditionTrue(crdv2.Available) {
					if util.IsPodConditionTrue(pod, corev1.ContainersReady) {
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
