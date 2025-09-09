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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	if status.CoreNodesStatus.UpdateRevision != "" && status.CoreNodesStatus.CurrentRevision == "" {
		status.CoreNodesStatus.CurrentRevision = status.CoreNodesStatus.UpdateRevision
	}
	if status.ReplicantNodesStatus.UpdateRevision != "" && status.ReplicantNodesStatus.CurrentRevision == "" {
		status.ReplicantNodesStatus.CurrentRevision = status.ReplicantNodesStatus.UpdateRevision
	}

	r.state = &reconcileState{}

	updateSts, currentSts, oldStsList := getStateFulSetList(r.ctx, u.Client, instance)
	if updateSts != nil {
		if currentSts == nil || (updateSts.UID != currentSts.UID && currentSts.Status.Replicas == 0) {
			var i int
			for i = 0; i < len(oldStsList); i++ {
				if oldStsList[i].Status.Replicas > 0 {
					currentSts = oldStsList[i]
					break
				}
			}
			if i == len(oldStsList) {
				currentSts = updateSts
			}
			status.CoreNodesStatus.CurrentRevision = currentSts.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		}
	}

	r.state.updateSts = updateSts
	r.state.currentSts = currentSts
	r.state.oldStsList = oldStsList

	updateRs, currentRs, oldRsList := getReplicaSetList(r.ctx, u.Client, instance)
	if updateRs != nil {
		if currentRs == nil || (updateRs.UID != currentRs.UID && currentRs.Status.Replicas == 0) {
			var i int
			for i = 0; i < len(oldRsList); i++ {
				if oldRsList[i].Status.Replicas > 0 {
					currentRs = oldRsList[i]
					break
				}
			}
			if i == len(oldRsList) {
				currentRs = updateRs
			}
			status.ReplicantNodesStatus.CurrentRevision = currentRs.Labels[appsv2beta1.LabelsPodTemplateHashKey]
		}
	}

	r.state.updateRs = updateRs
	r.state.currentRs = currentRs
	r.state.oldRsList = oldRsList

	// check emqx node status
	if r.api != nil {
		err := u.getEMQXNodes(r, instance)
		if err != nil {
			u.EventRecorder.Event(instance, corev1.EventTypeWarning, "FailedToGetNodeStatuses", err.Error())
		}
	}

	if currentSts != nil {
		status.CoreNodesStatus.CurrentReplicas = currentSts.Status.Replicas
	}
	if updateSts != nil {
		status.CoreNodesStatus.UpdateReplicas = updateSts.Status.Replicas
	}
	status.CoreNodesStatus.ReadyReplicas = 0
	for _, node := range status.CoreNodes {
		if node.NodeStatus == "running" {
			status.CoreNodesStatus.ReadyReplicas++
		}
	}

	if currentRs != nil {
		status.ReplicantNodesStatus.CurrentReplicas = currentRs.Status.Replicas
	}
	if updateRs != nil {
		status.ReplicantNodesStatus.UpdateReplicas = updateRs.Status.Replicas
	}
	status.ReplicantNodesStatus.ReadyReplicas = 0
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

func (u *updateStatus) getEMQXNodes(r *reconcileRound, instance *appsv2beta1.EMQX) error {
	emqxNodes, err := getEMQXNodesByAPI(r.api)
	if err != nil {
		return emperror.Wrap(err, "failed to get node statues by API")
	}

	list := &corev1.PodList{}
	_ = u.Client.List(r.ctx, list,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(appsv2beta1.DefaultLabels(instance)),
	)

	status := &instance.Status
	for _, node := range emqxNodes {
		for _, pod := range list.Items {
			controllerRef := metav1.GetControllerOf(&pod)
			if controllerRef == nil {
				continue
			}

			pod := pod.DeepCopy()
			r.state.pods = append(r.state.pods, pod)

			host := strings.Split(node.Node[strings.Index(node.Node, "@")+1:], ":")[0]
			if node.Role == "core" && strings.HasPrefix(host, pod.Name) {
				node.PodName = pod.Name
				status.CoreNodes = append(status.CoreNodes, node)
				r.state.corePods[node.Node] = pod
			}

			if node.Role == "replicant" && host == pod.Status.PodIP {
				node.PodName = pod.Name
				status.ReplicantNodes = append(status.ReplicantNodes, node)
				r.state.replicantPods[node.Node] = pod
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
