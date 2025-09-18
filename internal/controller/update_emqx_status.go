package controller

import (
	"sort"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type updateStatus struct {
	*EMQXReconciler
}

func (u *updateStatus) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	status := &instance.Status

	status.CoreNodesStatus.Replicas = *instance.Spec.CoreTemplate.Spec.Replicas
	if instance.Spec.ReplicantTemplate != nil {
		status.ReplicantNodesStatus.Replicas = *instance.Spec.ReplicantTemplate.Spec.Replicas
	}

	currentCoreSet, updateCoreSet := switchCoreSet(r, instance)
	currentReplicantSet, updateReplicantSet := switchReplicantSet(r, instance)

	status.CoreNodesStatus.ReadyReplicas = 0
	if currentCoreSet != nil {
		status.CoreNodesStatus.CurrentReplicas = currentCoreSet.Status.Replicas
	}
	if updateCoreSet != nil {
		status.CoreNodesStatus.UpdateReplicas = updateCoreSet.Status.Replicas
	}

	status.ReplicantNodesStatus.ReadyReplicas = 0
	if currentReplicantSet != nil {
		status.ReplicantNodesStatus.CurrentReplicas = currentReplicantSet.Status.Replicas
	}
	if updateReplicantSet != nil {
		status.ReplicantNodesStatus.UpdateReplicas = updateReplicantSet.Status.Replicas
	}

	// check emqx node status
	if r.api != nil {
		err := u.getEMQXNodes(r, instance)
		if err != nil {
			return subResult{err: emperror.Wrap(err, "failed to get node status")}
		}
	}
	for _, node := range status.CoreNodes {
		if node.NodeStatus == "running" {
			status.CoreNodesStatus.ReadyReplicas++
		}
	}
	for _, node := range status.ReplicantNodes {
		if node.NodeStatus == "running" {
			status.ReplicantNodesStatus.ReadyReplicas++
		}
	}

	if r.api != nil {
		nodeEvacuationsStatus, err := api.NodeEvacuationStatus(r.api)
		if err == nil {
			status.NodeEvacuationsStatus = nodeEvacuationsStatus
		} else {
			return subResult{err: emperror.Wrap(err, "failed to get node evacuation status")}
		}
	}

	// Reflect the status of the DS replication in the resource status.
	dsStatus := appsv2beta1.DSReplicationStatus{
		DBs: []appsv2beta1.DSDBReplicationStatus{},
	}

	var dsReplicationStatus api.DSReplicationStatus
	if r.api != nil {
		var err error
		dsReplicationStatus, err = api.GetDSReplicationStatus(r.api)
		if err != nil {
			return subResult{err: emperror.Wrap(err, "failed to get DS replication status")}
		}
	}
	for _, db := range dsReplicationStatus.DBs {
		minReplicas := 0
		maxReplicas := 0
		numTransitions := 0
		numShardReplicas := 0
		lostShardReplicas := 0
		if len(db.Shards) > 0 {
			minReplicas = len(db.Shards[0].Replicas)
			maxReplicas = len(db.Shards[0].Replicas)
		}
		for _, shard := range db.Shards {
			minReplicas = min(minReplicas, len(shard.Replicas))
			maxReplicas = max(maxReplicas, len(shard.Replicas))
			numTransitions += len(shard.Transitions)
			numShardReplicas += len(shard.Replicas)
			for _, replica := range shard.Replicas {
				if replica.Status == "lost" {
					lostShardReplicas += 1
				}
			}
		}
		dsStatus.DBs = append(dsStatus.DBs, appsv2beta1.DSDBReplicationStatus{
			Name:              db.Name,
			NumShards:         int32(len(db.Shards)),
			NumShardReplicas:  int32(numShardReplicas),
			LostShardReplicas: int32(lostShardReplicas),
			NumTransitions:    int32(numTransitions),
			MinReplicas:       int32(minReplicas),
			MaxReplicas:       int32(maxReplicas),
		})
	}
	status.DSReplication = dsStatus

	// update status condition
	u.updateStatusCondition(r, instance)

	if err := u.Client.Status().Update(r.ctx, instance); err != nil {
		return subResult{err: emperror.Wrap(err, "failed to update status")}
	}
	return subResult{}
}

func (u *updateStatus) updateStatusCondition(r *reconcileRound, instance *appsv2beta1.EMQX) {
	status := &instance.Status

	hasReplicants := appsv2beta1.IsExistReplicant(instance)

	condition := status.GetLastTrueCondition()
	if condition == nil {
		instance.Status.SetTrueCondition(appsv2beta1.Initialized)
		u.updateStatusCondition(r, instance)
		return
	}

	switch condition.Type {

	case appsv2beta1.Initialized:
		updateSts := r.state.updateCoreSet(instance)
		if updateSts != nil {
			u.statusTransition(r, instance, appsv2beta1.CoreNodesProgressing)
		}

	case appsv2beta1.CoreNodesProgressing:
		updateSts := r.state.updateCoreSet(instance)
		if updateSts != nil &&
			updateSts.Status.ReadyReplicas > 0 &&
			updateSts.Status.ReadyReplicas == status.CoreNodesStatus.UpdateReplicas {
			u.statusTransition(r, instance, appsv2beta1.CoreNodesReady)
		}

	case appsv2beta1.CoreNodesReady:
		if hasReplicants {
			u.statusTransition(r, instance, appsv2beta1.ReplicantNodesProgressing)
		} else {
			u.statusTransition(r, instance, appsv2beta1.Available)
		}

	case appsv2beta1.ReplicantNodesProgressing:
		if hasReplicants {
			updateRs := r.state.updateReplicantSet(instance)
			if updateRs != nil &&
				updateRs.Status.ReadyReplicas > 0 &&
				updateRs.Status.ReadyReplicas == status.ReplicantNodesStatus.UpdateReplicas {
				u.statusTransition(r, instance, appsv2beta1.ReplicantNodesReady)
			}
		} else {
			u.resetConditions(r, instance, "NoReplicants")
		}

	case appsv2beta1.ReplicantNodesReady:
		if hasReplicants {
			u.statusTransition(r, instance, appsv2beta1.Available)
		} else {
			u.resetConditions(r, instance, "NoReplicants")
		}

	case appsv2beta1.Available:
		if status.CoreNodesStatus.UpdateReplicas != status.CoreNodesStatus.Replicas ||
			status.CoreNodesStatus.ReadyReplicas != status.CoreNodesStatus.Replicas ||
			status.CoreNodesStatus.UpdateRevision != status.CoreNodesStatus.CurrentRevision {
			break
		}

		if hasReplicants {
			if status.ReplicantNodesStatus.UpdateReplicas != status.ReplicantNodesStatus.Replicas ||
				status.ReplicantNodesStatus.ReadyReplicas != status.ReplicantNodesStatus.Replicas ||
				status.ReplicantNodesStatus.UpdateRevision != status.ReplicantNodesStatus.CurrentRevision {
				break
			}
		}

		status.SetCondition(metav1.Condition{
			Type:    appsv2beta1.Ready,
			Status:  metav1.ConditionTrue,
			Reason:  appsv2beta1.Ready,
			Message: "Cluster is ready",
		})

	case appsv2beta1.Ready:
		updateSts := r.state.updateCoreSet(instance)
		if updateSts != nil &&
			updateSts.Status.ReadyReplicas != status.CoreNodesStatus.Replicas {
			u.resetConditions(r, instance, "CoreNodesNotReady")
			return
		}

		if hasReplicants {
			updateRs := r.state.updateReplicantSet(instance)
			if updateRs != nil &&
				updateRs.Status.ReadyReplicas != status.ReplicantNodesStatus.Replicas {
				u.resetConditions(r, instance, "ReplicantNodesNotReady")
				return
			}
		}
	}
}

func (u *updateStatus) resetConditions(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	reason string,
) {
	hasReplicants := appsv2beta1.IsExistReplicant(instance)
	if !hasReplicants {
		instance.Status.RemoveCondition(appsv2beta1.ReplicantNodesProgressing)
		instance.Status.RemoveCondition(appsv2beta1.ReplicantNodesReady)
	}
	instance.Status.ResetConditions(reason)
	u.updateStatusCondition(r, instance)
}

func (u *updateStatus) statusTransition(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
	conditionType string,
) {
	instance.Status.SetTrueCondition(conditionType)
	u.updateStatusCondition(r, instance)
}

func switchCoreSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
) (*appsv1.StatefulSet, *appsv1.StatefulSet) {
	current := r.state.currentCoreSet(instance)
	update := r.state.updateCoreSet(instance)
	if (current == nil || current.Status.Replicas == 0) && update != nil {
		current = nil
		for _, coreSet := range r.state.coreSets {
			// Adopt oldest non-empty coreSet if there are more than 2 (current and update) coreSets:
			if coreSet.UID != update.UID && coreSet.Status.Replicas > 0 {
				current = coreSet
				break
			}
		}
		if current == nil {
			current = update
		}
	}
	if current != nil {
		instance.Status.CoreNodesStatus.CurrentRevision = current.Labels[appsv2beta1.LabelsPodTemplateHashKey]
	}
	return current, update
}

func switchReplicantSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
) (*appsv1.ReplicaSet, *appsv1.ReplicaSet) {
	current := r.state.currentReplicantSet(instance)
	update := r.state.updateReplicantSet(instance)
	if (current == nil || current.Status.Replicas == 0) && update != nil {
		current = nil
		for _, replicantSet := range r.state.replicantSets {
			// Adopt oldest non-empty replicantSet if there are more than 2 (current and update) replicantSets:
			if replicantSet.UID != update.UID && replicantSet.Status.Replicas > 0 {
				current = replicantSet
				break
			}
		}
		if current == nil {
			current = update
		}
	}
	if current != nil {
		instance.Status.ReplicantNodesStatus.CurrentRevision = current.Labels[appsv2beta1.LabelsPodTemplateHashKey]
	}
	return current, update
}

func (u *updateStatus) getEMQXNodes(r *reconcileRound, instance *appsv2beta1.EMQX) error {
	emqxNodes, err := api.Nodes(r.api)
	if err != nil {
		return emperror.Wrap(err, "failed to get node statues by API")
	}

	status := &instance.Status
	status.CoreNodes = []appsv2beta1.EMQXNode{}
	status.ReplicantNodes = []appsv2beta1.EMQXNode{}
	for _, node := range emqxNodes {
		for _, pod := range r.state.pods {
			host := strings.Split(node.Node[strings.Index(node.Node, "@")+1:], ":")[0]
			if node.Role == "core" && strings.HasPrefix(host, pod.Name) {
				node.PodName = pod.Name
				status.CoreNodes = append(status.CoreNodes, node)
			}
			if node.Role == "replicant" && host == pod.Status.PodIP {
				node.PodName = pod.Name
				status.ReplicantNodes = append(status.ReplicantNodes, node)
			}
		}
	}

	sort.Slice(status.CoreNodes, func(i, j int) bool {
		return status.CoreNodes[i].Uptime < status.CoreNodes[j].Uptime
	})
	sort.Slice(status.ReplicantNodes, func(i, j int) bool {
		return status.ReplicantNodes[i].Uptime < status.ReplicantNodes[j].Uptime
	})

	return nil
}
