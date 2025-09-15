package controller

import (
	"fmt"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type syncReplicantSets struct {
	*EMQXReconciler
}

type scaleDownReplicant struct {
	Pod    *corev1.Pod
	Reason string
}

func (s *syncReplicantSets) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	updateRs := r.state.updateReplicantSet(instance)
	currentRs := r.state.currentReplicantSet(instance)
	if updateRs == nil || currentRs == nil {
		return subResult{}
	}
	if updateRs.UID != currentRs.UID {
		return s.migrateSet(r, instance, currentRs)
	}
	return subResult{}
}

// Orchestrates gradual scale down of the old replicaSet, by migrating workloads to the new replicaSet.
func (s *syncReplicantSets) migrateSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	current *appsv1.ReplicaSet,
) subResult {
	admission, err := s.chooseScaleDownReplicant(r, instance, current)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to check if old replicaSet can be scaled down")}
	}
	if admission.Pod != nil {
		if admission.Pod.Annotations == nil {
			admission.Pod.Annotations = make(map[string]string)
		}

		// https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/#pod-deletion-cost
		admission.Pod.Annotations["controller.kubernetes.io/pod-deletion-cost"] = "-99999"
		if err := s.Client.Update(r.ctx, admission.Pod); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update pod deletion cost")}
		}

		// https://github.com/emqx/emqx-operator/issues/1105
		*current.Spec.Replicas = *current.Spec.Replicas - 1
		if err := s.Client.Update(r.ctx, current); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale down old replicaSet")}
		}
	}
	return subResult{}
}

func (s *syncReplicantSets) chooseScaleDownReplicant(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	current *appsv1.ReplicaSet,
) (scaleDownReplicant, error) {
	var err error
	var scaleDownPod *corev1.Pod
	var scaleDownNodeName string
	var scaleDownPodInfo *appsv2beta1.EMQXNode
	status := &instance.Status

	// Disallow scaling down the replicaSet if the instance just recently became ready.
	if !checkInitialDelaySecondsReady(instance) {
		return scaleDownReplicant{Reason: "instance is not ready"}, nil
	}

	// Nothing to do if the replicaSet has no pods.
	currentPods := r.state.podsManagedBy(current.UID)
	sortByName(currentPods)
	if len(currentPods) == 0 {
		return scaleDownReplicant{Reason: "no more pods"}, nil
	}

	// If a pod is already being deleted, return it.
	for _, pod := range currentPods {
		if pod.DeletionTimestamp != nil {
			return scaleDownReplicant{Reason: "pod deletion in progress"}, nil
		}
		if _, ok := pod.Annotations["controller.kubernetes.io/pod-deletion-cost"]; ok {
			return scaleDownReplicant{Pod: pod, Reason: "pod already marked for deletion"}, nil
		}
	}

	if len(status.NodeEvacuationsStatus) > 0 {
		if status.NodeEvacuationsStatus[0].State != "prohibiting" {
			return scaleDownReplicant{Reason: "node evacuation is still in progress"}, nil
		}
		scaleDownNodeName = status.NodeEvacuationsStatus[0].Node
		for _, node := range status.ReplicantNodes {
			if node.Node == scaleDownNodeName {
				scaleDownPodInfo = &node
				scaleDownPod = r.state.podWithName(node.PodName)
				break
			}
		}
	} else {
		// If there is no node evacuation, return the oldest pod.
		scaleDownPod = currentPods[0]
		scaleDownNodeName = fmt.Sprintf("emqx@%s", scaleDownPod.Status.PodIP)
		scaleDownPodInfo, err = api.NodeInfo(r.api, scaleDownNodeName)
		if err != nil {
			return scaleDownReplicant{}, emperror.Wrap(err, "failed to get node info")
		}
		// If the pod is already stopped, return it.
		if scaleDownPodInfo.NodeStatus == "stopped" {
			return scaleDownReplicant{Pod: scaleDownPod, Reason: "pod is already stopped"}, nil
		}
	}

	// Disallow scaling down the pod that is still a DS replication site.
	// While replicants are not supposed to be DS replication sites, check it for safety.
	dsCondition := util.FindPodCondition(scaleDownPod, appsv2beta1.DSReplicationSite)
	if dsCondition != nil && dsCondition.Status != corev1.ConditionFalse {
		return scaleDownReplicant{Reason: "pod is still a DS replication site"}, nil
	}

	// If the pod has at least one session, start node evacuation.
	if scaleDownPodInfo.Session > 0 {
		strategy := instance.Spec.UpdateStrategy.EvacuationStrategy
		migrateTo := migrationTargetNodes(r, instance)
		if err := api.StartEvacuation(r.api, strategy, migrateTo, scaleDownNodeName); err != nil {
			return scaleDownReplicant{}, emperror.Wrap(err, "failed to start node evacuation")
		}
		s.EventRecorder.Event(instance, corev1.EventTypeNormal, "NodeEvacuation", fmt.Sprintf("Node %s is being evacuated", scaleDownNodeName))
		return scaleDownReplicant{Reason: "node needs to be evacuated"}, nil
	}

	// With no sessions
	if !checkWaitTakeoverReady(instance, getEventList(r.ctx, s.Clientset, current)) {
		return scaleDownReplicant{Reason: "node evacuation just finished"}, nil
	}

	return scaleDownReplicant{Pod: scaleDownPod}, nil
}
