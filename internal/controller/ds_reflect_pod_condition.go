package controller

import (
	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	req "github.com/emqx/emqx-operator/internal/requester"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type dsReflectPodCondition struct {
	*EMQXReconciler
}

func (u *dsReflectPodCondition) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// If there's no EMQX API to query, skip the reconciliation.
	if r.api == nil {
		return subResult{}
	}

	req := u.getSuitableRequester(r, instance)

	// If EMQX DS API is not available, skip this reconciliation step.
	// We need this API to be available to ask it about replication status.
	cluster, err := api.GetCluster(req)
	if err != nil && emperror.Is(err, api.ErrorNotFound) {
		return subResult{}
	}
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS cluster status")}
	}

	for _, pod := range r.state.pods {
		node := u.findNode(instance, pod)
		if node == nil {
			continue
		}
		condition := corev1.PodCondition{
			Type:               appsv2beta1.DSReplicationSite,
			Status:             corev1.ConditionUnknown,
			LastTransitionTime: metav1.Now(),
		}
		site := cluster.FindSite(node.Node)
		if site != nil {
			if len(site.Shards) > 0 {
				condition.Status = corev1.ConditionTrue
			} else {
				condition.Status = corev1.ConditionFalse
			}
		}
		existing := util.FindPodCondition(pod, appsv2beta1.DSReplicationSite)
		if existing == nil || existing.Status != condition.Status {
			err := util.UpdatePodCondition(r.ctx, u.Client, pod, condition)
			if err != nil {
				return subResult{err: emperror.Wrapf(err, "failed to update pod %s status", pod.Name)}
			}
		}
	}

	return subResult{}
}

func (u *dsReflectPodCondition) findNode(instance *appsv2beta1.EMQX, pod *corev1.Pod) *appsv2beta1.EMQXNode {
	for _, node := range instance.Status.CoreNodes {
		if node.PodName == pod.Name {
			return &node
		}
	}
	for _, node := range instance.Status.ReplicantNodes {
		if node.PodName == pod.Name {
			return &node
		}
	}
	return nil
}

func (u *dsReflectPodCondition) getSuitableRequester(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
) req.RequesterInterface {
	// Prefer node that is part of "update" StatefulSet (if any).
	for _, core := range instance.Status.CoreNodes {
		pod := r.state.podWithName(core.PodName)
		if r.state.partOfUpdateSet(pod, instance) {
			ready := util.FindPodCondition(pod, corev1.ContainersReady)
			if ready != nil && ready.Status == corev1.ConditionTrue {
				return r.api.SwitchHost(pod.Status.PodIP, pod.Name)
			}
		}
	}
	// If no suitable pod found, return the original requester.
	return r.api
}
