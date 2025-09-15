package controller

import (
	"fmt"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
)

type syncCoreSets struct {
	*EMQXReconciler
}

type scaleDownCore struct {
	Pod    *corev1.Pod
	Reason string
}

func (s *syncCoreSets) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	updateSts := r.state.updateCoreSet(instance)
	currentSts := r.state.currentCoreSet(instance)
	if updateSts == nil || currentSts == nil {
		return subResult{}
	}
	if updateSts.UID != currentSts.UID {
		return s.migrateSet(r, instance, currentSts)
	}
	return s.scaleDownSet(r, instance, currentSts)
}

// Orchestrates gradual scale down of the old statefulSet, by migrating workloads to the new statefulSet.
func (s *syncCoreSets) migrateSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	current *appsv1.StatefulSet,
) subResult {
	admission, err := s.chooseScaleDownCore(r, instance, current)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to check if old statefulSet can be scaled down")}
	}
	if admission.Pod != nil {
		*current.Spec.Replicas = *current.Spec.Replicas - 1
		if err := s.Client.Update(r.ctx, current); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale down old statefulSet")}
		}
	}
	return subResult{}
}

// Scale up or down the existing statefulSet.
func (s *syncCoreSets) scaleDownSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	current *appsv1.StatefulSet,
) subResult {
	desiredReplicas := *instance.Spec.CoreTemplate.Spec.Replicas
	currentReplicas := *current.Spec.Replicas

	if currentReplicas < desiredReplicas {
		*current.Spec.Replicas = desiredReplicas
		if err := s.Client.Update(r.ctx, current); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale up statefulSet")}
		}
		return subResult{}
	}

	if currentReplicas > desiredReplicas {
		admission, err := s.chooseScaleDownCore(r, instance, current)
		if err != nil {
			return subResult{err: emperror.Wrap(err, "failed to check if statefulSet can be scaled down")}
		}
		if admission.Pod != nil {
			*current.Spec.Replicas = *current.Spec.Replicas - 1
			if err := s.Client.Update(r.ctx, current); err != nil {
				return subResult{err: emperror.Wrap(err, "failed to scale down statefulSet")}
			}
			return subResult{}
		}
	}

	return subResult{}
}

func (s *syncCoreSets) chooseScaleDownCore(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	current *appsv1.StatefulSet,
) (scaleDownCore, error) {
	// Disallow scaling down the statefulSet if replcants replicaSet is still updating.
	status := &instance.Status
	if appsv2beta1.IsExistReplicant(instance) {
		if status.ReplicantNodesStatus.CurrentRevision != status.ReplicantNodesStatus.UpdateRevision {
			return scaleDownCore{Reason: "replicant replicaSet is still updating"}, nil
		}
	}

	if !checkInitialDelaySecondsReady(instance) {
		return scaleDownCore{Reason: "instance is not ready"}, nil
	}

	if len(status.NodeEvacuationsStatus) > 0 {
		if status.NodeEvacuationsStatus[0].State != "prohibiting" {
			return scaleDownCore{Reason: "node evacuation is still in progress"}, nil
		}
	}

	// Get the pod to be scaled down next.
	scaleDownPod := &corev1.Pod{}
	err := s.Client.Get(r.ctx, instance.NamespacedName(
		fmt.Sprintf("%s-%d", current.Name, *current.Spec.Replicas-1),
	), scaleDownPod)

	// No more pods, no need to scale down.
	if err != nil && k8sErrors.IsNotFound(err) {
		return scaleDownCore{Reason: "no more pods"}, nil
	}

	// Disallow scaling down the pod that is already being deleted.
	if scaleDownPod.DeletionTimestamp != nil {
		return scaleDownCore{Reason: "pod deletion in progress"}, nil
	}

	// Disallow scaling down the pod that is still a DS replication site.
	// Only if DS is enabled in the current, most recent EMQX config.
	// Otherwise, if the user has disabled DS, the data is apparently no longer
	// needs to be preserved.
	if r.conf.IsDSEnabled() {
		dsCondition := util.FindPodCondition(scaleDownPod, appsv2beta1.DSReplicationSite)
		if dsCondition != nil && dsCondition.Status != corev1.ConditionFalse {
			return scaleDownCore{Reason: "pod is still a DS replication site"}, nil
		}
	}

	// Get the node info of the pod to be scaled down.
	scaleDownNodeName := fmt.Sprintf("emqx@%s.%s.%s.svc.cluster.local", scaleDownPod.Name, current.Spec.ServiceName, current.Namespace)
	scaleDownNode, err := api.NodeInfo(r.api, scaleDownNodeName)
	if err != nil {
		return scaleDownCore{}, emperror.Wrap(err, "failed to get node info by API")
	}

	// Scale down the node that is already stopped.
	if scaleDownNode.NodeStatus == "stopped" {
		return scaleDownCore{Pod: scaleDownPod, Reason: "node is already stopped"}, nil
	}

	// Disallow scaling down the node that has at least one session.
	if scaleDownNode.Session > 0 {
		strategy := instance.Spec.UpdateStrategy.EvacuationStrategy
		migrateTo := migrationTargetNodes(r, instance)
		if err := api.StartEvacuation(r.api, strategy, migrateTo, scaleDownNode.Node); err != nil {
			return scaleDownCore{}, emperror.Wrap(err, "failed to start node evacuation")
		}
		s.EventRecorder.Event(instance, corev1.EventTypeNormal, "NodeEvacuation", fmt.Sprintf("Node %s is being evacuated", scaleDownNode.Node))
		return scaleDownCore{Reason: "node needs to be evacuated"}, nil
	}

	return scaleDownCore{Pod: scaleDownPod}, nil
}

// Returns the list of nodes to migrate workloads to.
func migrationTargetNodes(r *reconcileRound, instance *appsv2beta1.EMQX) []string {
	targets := []string{}
	if appsv2beta1.IsExistReplicant(instance) {
		for _, node := range instance.Status.ReplicantNodes {
			pod := r.state.podWithName(node.PodName)
			if r.state.partOfUpdateSet(pod, instance) {
				targets = append(targets, node.Node)
			}
		}
	} else {
		for _, node := range instance.Status.CoreNodes {
			pod := r.state.podWithName(node.PodName)
			if r.state.partOfUpdateSet(pod, instance) {
				targets = append(targets, node.Node)
			}
		}
	}
	return targets
}
