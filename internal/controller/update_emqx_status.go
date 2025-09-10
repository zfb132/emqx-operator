package controller

import (
	"encoding/json"
	"sort"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/emqx/emqx-operator/internal/controller/ds"
	req "github.com/emqx/emqx-operator/internal/requester"
	"github.com/tidwall/gjson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
			u.EventRecorder.Event(instance, corev1.EventTypeWarning, "FailedToGetNodeStatuses", err.Error())
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
		nodeEvacuationsStatus, err := getNodeEvacuationStatusByAPI(r.api)
		if err == nil {
			status.NodeEvacuationsStatus = nodeEvacuationsStatus
		} else {
			u.EventRecorder.Event(instance, corev1.EventTypeWarning, "FailedToGetNodeEvacuationStatuses", err.Error())
		}
	}

	// Reflect the status of the DS replication in the resource status.
	dsStatus := appsv2beta1.DSReplicationStatus{
		DBs: []appsv2beta1.DSDBReplicationStatus{},
	}

	var dsReplicationStatus ds.DSReplicationStatus
	if r.api != nil {
		var err error
		dsReplicationStatus, err = ds.GetReplicationStatus(r.api)
		if err != nil {
			u.EventRecorder.Event(instance, corev1.EventTypeWarning, "FailedToGetDSReplicationStatus", err.Error())
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
	newEMQXStatusMachine(u.Client, instance).NextStatus(r.ctx)

	if err := u.Client.Status().Update(r.ctx, instance); err != nil {
		return subResult{err: emperror.Wrap(err, "failed to update status")}
	}
	return subResult{}
}

func switchCoreSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
) (*appsv1.StatefulSet, *appsv1.StatefulSet) {
	status := &instance.Status
	current := r.state.currentCoreSet(instance)
	update := r.state.updateCoreSet(instance)
	if (current == nil || current.Status.Replicas == 0) && update != nil {
		status.CoreNodesStatus.CurrentRevision = status.CoreNodesStatus.UpdateRevision
		return update, update
	}
	return current, update
}

func switchReplicantSet(
	r *reconcileRound,
	instance *appsv2beta1.EMQX,
) (*appsv1.ReplicaSet, *appsv1.ReplicaSet) {
	status := &instance.Status
	current := r.state.currentReplicantSet(instance)
	update := r.state.updateReplicantSet(instance)
	if (current == nil || current.Status.Replicas == 0) && update != nil {
		status.ReplicantNodesStatus.CurrentRevision = status.ReplicantNodesStatus.UpdateRevision
		return update, update
	}
	return current, update
}

func (u *updateStatus) getEMQXNodes(r *reconcileRound, instance *appsv2beta1.EMQX) error {
	emqxNodes, err := getEMQXNodesByAPI(r.api)
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

func getEMQXNodesByAPI(req req.RequesterInterface) ([]appsv2beta1.EMQXNode, error) {
	url := req.GetURL("api/v5/nodes")

	resp, body, err := req.Request("GET", url, nil, nil)
	if err != nil {
		return nil, emperror.Wrapf(err, "failed to get API %s", url.String())
	}
	if resp.StatusCode != 200 {
		return nil, emperror.Errorf("failed to get API %s, status : %s, body: %s", url.String(), resp.Status, body)
	}

	nodeStatuses := []appsv2beta1.EMQXNode{}
	if err := json.Unmarshal(body, &nodeStatuses); err != nil {
		return nil, emperror.Wrap(err, "failed to unmarshal node statuses")
	}
	return nodeStatuses, nil
}

func getNodeEvacuationStatusByAPI(req req.RequesterInterface) ([]appsv2beta1.NodeEvacuationStatus, error) {
	url := req.GetURL("api/v5/load_rebalance/global_status")
	resp, body, err := req.Request("GET", url, nil, nil)
	if err != nil {
		return nil, emperror.Wrapf(err, "failed to get API %s", url.String())
	}
	if resp.StatusCode != 200 {
		return nil, emperror.Errorf("failed to get API %s, status : %s, body: %s", url.String(), resp.Status, body)
	}

	nodeEvacuationStatuses := []appsv2beta1.NodeEvacuationStatus{}
	data := gjson.GetBytes(body, "evacuations")
	if err := json.Unmarshal([]byte(data.Raw), &nodeEvacuationStatuses); err != nil {
		return nil, emperror.Wrap(err, "failed to unmarshal node statuses")
	}
	return nodeEvacuationStatuses, nil
}
