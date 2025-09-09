package controller

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	req "github.com/emqx/emqx-operator/internal/requester"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type syncPods struct {
	*EMQXReconciler
}

type scaleDownAdmission struct {
	Pod    *corev1.Pod
	Reason string
}

func (s *syncPods) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	result := subResult{}

	if r == nil {
		return result
	}
	if !instance.Status.IsConditionTrue(appsv2beta1.Available) {
		return result
	}

	result = s.reconcileReplicaSets(r, instance)
	if result.err != nil {
		return result
	}

	result = s.reconcileStatefulSets(r, instance)
	return result
}

func (s *syncPods) reconcileReplicaSets(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	if r.state.updateRs == nil || r.state.currentRs == nil {
		return subResult{}
	}
	if r.state.updateRs.UID != r.state.currentRs.UID {
		return s.migrateReplicaSet(r, instance)
	}
	return subResult{}
}

func (s *syncPods) reconcileStatefulSets(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	if r.state.updateSts == nil || r.state.currentSts == nil {
		return subResult{}
	}
	if r.state.updateSts.UID != r.state.currentSts.UID {
		return s.migrateStatefulSet(r, instance)
	}
	return s.scaleStatefulSet(r, instance)
}

// Orchestrates gradual scale down of the old replicaSet, by migrating workloads to the new replicaSet.
func (s *syncPods) migrateReplicaSet(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	admission, err := s.canScaleDownReplicaSet(r, instance)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to check if old replicaSet can be scaled down")}
	}
	if admission.Pod != nil && admission.Pod.DeletionTimestamp == nil {
		if admission.Pod.Annotations == nil {
			admission.Pod.Annotations = make(map[string]string)
		}

		// https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/#pod-deletion-cost
		admission.Pod.Annotations["controller.kubernetes.io/pod-deletion-cost"] = "-99999"
		if err := s.Client.Update(r.ctx, admission.Pod); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update pod deletion cost")}
		}

		// https://github.com/emqx/emqx-operator/issues/1105
		var currentRs *appsv1.ReplicaSet = r.state.currentRs
		*currentRs.Spec.Replicas = *currentRs.Spec.Replicas - 1
		if err := s.Client.Update(r.ctx, currentRs); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale down old replicaSet")}
		}
	}
	return subResult{}
}

// Orchestrates gradual scale down of the old statefulSet, by migrating workloads to the new statefulSet.
func (s *syncPods) migrateStatefulSet(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	admission, err := s.canScaleDownStatefulSet(r, instance)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to check if old statefulSet can be scaled down")}
	}
	if admission.Pod != nil {
		var currentSts *appsv1.StatefulSet = r.state.currentSts
		*currentSts.Spec.Replicas = *currentSts.Spec.Replicas - 1
		if err := s.Client.Update(r.ctx, currentSts); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale down old statefulSet")}
		}
	}
	return subResult{}
}

// Scale up or down the existing statefulSet.
func (s *syncPods) scaleStatefulSet(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	var sts *appsv1.StatefulSet = r.state.currentSts
	desiredReplicas := *instance.Spec.CoreTemplate.Spec.Replicas
	currentReplicas := *sts.Spec.Replicas

	if currentReplicas < desiredReplicas {
		*sts.Spec.Replicas = desiredReplicas
		if err := s.Client.Update(r.ctx, sts); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to scale up statefulSet")}
		}
		return subResult{}
	}

	if currentReplicas > desiredReplicas {
		admission, err := s.canScaleDownStatefulSet(r, instance)
		if err != nil {
			return subResult{err: emperror.Wrap(err, "failed to check if statefulSet can be scaled down")}
		}
		if admission.Pod != nil {
			*sts.Spec.Replicas = *sts.Spec.Replicas - 1
			if err := s.Client.Update(r.ctx, sts); err != nil {
				return subResult{err: emperror.Wrap(err, "failed to scale down statefulSet")}
			}
			return subResult{}
		}
	}

	return subResult{}
}

func (s *syncPods) canScaleDownReplicaSet(r *reconcileRound, instance *appsv2beta1.EMQX) (scaleDownAdmission, error) {
	var err error
	var currentRs *appsv1.ReplicaSet = r.state.currentRs
	var scaleDownPod *corev1.Pod
	var scaleDownNodeName string
	var scaleDownPodInfo *appsv2beta1.EMQXNode
	status := &instance.Status

	// Disallow scaling down the replicaSet if the instance just recently became ready.
	if !checkInitialDelaySecondsReady(instance) {
		return scaleDownAdmission{Reason: "instance is not ready"}, nil
	}

	// Nothing to do if the replicaSet has no pods.
	currentPods := listPodsManagedBy(r.ctx, s.Client, instance, currentRs.UID)
	sort.Sort(PodsByNameOlder(currentPods))
	if len(currentPods) == 0 {
		return scaleDownAdmission{Reason: "no more pods"}, nil
	}

	// If a pod is already being deleted, return it.
	for _, pod := range currentPods {
		if pod.DeletionTimestamp != nil {
			return scaleDownAdmission{Pod: pod, Reason: "pod is already being deleted"}, nil
		}
		if _, ok := pod.Annotations["controller.kubernetes.io/pod-deletion-cost"]; ok {
			return scaleDownAdmission{Pod: pod, Reason: "pod is already being deleted"}, nil
		}
	}

	if len(status.NodeEvacuationsStatus) > 0 {
		if status.NodeEvacuationsStatus[0].State != "prohibiting" {
			return scaleDownAdmission{Reason: "node evacuation is still in progress"}, nil
		}
		scaleDownNodeName = status.NodeEvacuationsStatus[0].Node
		for _, node := range status.ReplicantNodes {
			if node.Node == scaleDownNodeName {
				scaleDownPodInfo = &node
				break
			}
		}
		for _, pod := range currentPods {
			if pod.Name == scaleDownPodInfo.PodName {
				scaleDownPod = pod
				break
			}
		}
	} else {
		// If there is no node evacuation, return the oldest pod.
		scaleDownPod = currentPods[0]
		scaleDownNodeName = fmt.Sprintf("emqx@%s", scaleDownPod.Status.PodIP)
		scaleDownPodInfo, err = getEMQXNodeInfoByAPI(r.api, scaleDownNodeName)
		if err != nil {
			return scaleDownAdmission{}, emperror.Wrap(err, "failed to get node info by API")
		}
		// If the pod is already stopped, return it.
		if scaleDownPodInfo.NodeStatus == "stopped" {
			return scaleDownAdmission{Pod: scaleDownPod, Reason: "pod is already stopped"}, nil
		}
	}

	// Disallow scaling down the pod that is still a DS replication site.
	// While replicants are not supposed to be DS replication sites, check it for safety.
	dsCondition := appsv2beta1.FindPodCondition(scaleDownPod, appsv2beta1.DSReplicationSite)
	if dsCondition != nil && dsCondition.Status != corev1.ConditionFalse {
		return scaleDownAdmission{Reason: "pod is still a DS replication site"}, nil
	}

	// If the pod has at least one session, start node evacuation.
	if scaleDownPodInfo.Session > 0 {
		migrateTo := s.migrationTargetNodes(r, instance)
		if err := startEvacuationByAPI(r.api, instance, migrateTo, scaleDownNodeName); err != nil {
			return scaleDownAdmission{}, emperror.Wrap(err, "failed to start node evacuation")
		}
		s.EventRecorder.Event(instance, corev1.EventTypeNormal, "NodeEvacuation", fmt.Sprintf("Node %s is being evacuated", scaleDownNodeName))
		return scaleDownAdmission{Reason: "node needs to be evacuated"}, nil
	}

	// With no sessions
	if !checkWaitTakeoverReady(instance, getEventList(r.ctx, s.Clientset, currentRs)) {
		return scaleDownAdmission{Reason: "node evacuation just finished"}, nil
	}

	return scaleDownAdmission{Pod: scaleDownPod}, nil
}

func (s *syncPods) canScaleDownStatefulSet(r *reconcileRound, instance *appsv2beta1.EMQX) (scaleDownAdmission, error) {
	// Disallow scaling down the statefulSet if replcants replicaSet is still updating.
	status := &instance.Status
	if appsv2beta1.IsExistReplicant(instance) {
		if status.ReplicantNodesStatus.CurrentRevision != status.ReplicantNodesStatus.UpdateRevision {
			return scaleDownAdmission{Reason: "replicant replicaSet is still updating"}, nil
		}
	}

	if !checkInitialDelaySecondsReady(instance) {
		return scaleDownAdmission{Reason: "instance is not ready"}, nil
	}

	if len(status.NodeEvacuationsStatus) > 0 {
		if status.NodeEvacuationsStatus[0].State != "prohibiting" {
			return scaleDownAdmission{Reason: "node evacuation is still in progress"}, nil
		}
	}

	// Get the pod to be scaled down next.
	var currentSts *appsv1.StatefulSet = r.state.currentSts
	scaleDownPod := &corev1.Pod{}
	err := s.Client.Get(r.ctx, instance.NamespacedName(
		fmt.Sprintf("%s-%d", currentSts.Name, *currentSts.Spec.Replicas-1),
	), scaleDownPod)

	// No more pods, no need to scale down.
	if err != nil && k8sErrors.IsNotFound(err) {
		return scaleDownAdmission{Reason: "no more pods"}, nil
	}

	// Disallow scaling down the pod that is already being deleted.
	if scaleDownPod.DeletionTimestamp != nil {
		return scaleDownAdmission{Reason: "pod deletion in progress"}, nil
	}

	// Disallow scaling down the pod that is still a DS replication site.
	// Only if DS is enabled in the current, most recent EMQX config.
	// Otherwise, if the user has disabled DS, the data is apparently no longer
	// needs to be preserved.
	if r.conf.IsDSEnabled() {
		dsCondition := appsv2beta1.FindPodCondition(scaleDownPod, appsv2beta1.DSReplicationSite)
		if dsCondition != nil && dsCondition.Status != corev1.ConditionFalse {
			return scaleDownAdmission{Reason: "pod is still a DS replication site"}, nil
		}
	}

	// Get the node info of the pod to be scaled down.
	scaleDownNodeName := fmt.Sprintf("emqx@%s.%s.%s.svc.cluster.local", scaleDownPod.Name, currentSts.Spec.ServiceName, currentSts.Namespace)
	scaleDownNode, err := getEMQXNodeInfoByAPI(r.api, scaleDownNodeName)
	if err != nil {
		return scaleDownAdmission{}, emperror.Wrap(err, "failed to get node info by API")
	}

	// Scale down the node that is already stopped.
	if scaleDownNode.NodeStatus == "stopped" {
		return scaleDownAdmission{Pod: scaleDownPod, Reason: "node is already stopped"}, nil
	}

	// Disallow scaling down the node that has at least one session.
	if scaleDownNode.Session > 0 {
		migrateTo := s.migrationTargetNodes(r, instance)
		if err := startEvacuationByAPI(r.api, instance, migrateTo, scaleDownNode.Node); err != nil {
			return scaleDownAdmission{}, emperror.Wrap(err, "failed to start node evacuation")
		}
		s.EventRecorder.Event(instance, corev1.EventTypeNormal, "NodeEvacuation", fmt.Sprintf("Node %s is being evacuated", scaleDownNode.Node))
		return scaleDownAdmission{Reason: "node needs to be evacuated"}, nil
	}

	// With no sessions
	if !checkWaitTakeoverReady(instance, getEventList(r.ctx, s.Clientset, currentSts)) {
		return scaleDownAdmission{Reason: "node evacuation just finished"}, nil
	}

	return scaleDownAdmission{Pod: scaleDownPod}, nil
}

// Returns the list of nodes to migrate workloads to.
func (s *syncPods) migrationTargetNodes(r *reconcileRound, instance *appsv2beta1.EMQX) []string {
	targets := []string{}
	if appsv2beta1.IsExistReplicant(instance) {
		if r.state.updateRs != nil {
			for _, node := range instance.Status.ReplicantNodes {
				pod := r.state.replicantPods[node.Node]
				controllerRef := metav1.GetControllerOf(pod)
				if controllerRef.UID == r.state.updateRs.UID {
					targets = append(targets, node.Node)
				}
			}
		}
	} else {
		if r.state.updateSts != nil {
			for _, node := range instance.Status.CoreNodes {
				pod := r.state.corePods[node.Node]
				controllerRef := metav1.GetControllerOf(pod)
				if controllerRef.UID == r.state.updateSts.UID {
					targets = append(targets, node.Node)
				}
			}
		}
	}
	return targets
}

func getEMQXNodeInfoByAPI(r req.RequesterInterface, nodeName string) (*appsv2beta1.EMQXNode, error) {
	url := r.GetURL(fmt.Sprintf("api/v5/nodes/%s", nodeName))

	resp, body, err := r.Request("GET", url, nil, nil)
	if err != nil {
		return nil, emperror.Wrap(err, "failed to get API api/v5/nodes")
	}
	if resp.StatusCode == 404 {
		return &appsv2beta1.EMQXNode{
			Node:       nodeName,
			NodeStatus: "stopped",
		}, nil
	}
	if resp.StatusCode != 200 {
		return nil, emperror.Errorf("failed to get API %s, status : %s, body: %s", url.String(), resp.Status, body)
	}

	nodeInfo := &appsv2beta1.EMQXNode{}
	if err := json.Unmarshal(body, &nodeInfo); err != nil {
		return nil, emperror.Wrap(err, "failed to unmarshal node statuses")
	}
	return nodeInfo, nil
}

func startEvacuationByAPI(r req.RequesterInterface, instance *appsv2beta1.EMQX, migrateTo []string, nodeName string) error {
	body := map[string]interface{}{
		"conn_evict_rate": instance.Spec.UpdateStrategy.EvacuationStrategy.ConnEvictRate,
		"sess_evict_rate": instance.Spec.UpdateStrategy.EvacuationStrategy.SessEvictRate,
		"migrate_to":      migrateTo,
	}
	if instance.Spec.UpdateStrategy.EvacuationStrategy.WaitTakeover > 0 {
		body["wait_takeover"] = instance.Spec.UpdateStrategy.EvacuationStrategy.WaitTakeover
	}
	// Specify `wait_health_check` if different from the default.
	if instance.Spec.UpdateStrategy.EvacuationStrategy.WaitHealthCheck != 60 {
		body["wait_health_check"] = fmt.Sprintf("%ds", instance.Spec.UpdateStrategy.EvacuationStrategy.WaitHealthCheck)
	}

	b, err := json.Marshal(body)
	if err != nil {
		return emperror.Wrap(err, "marshal body failed")
	}

	url := r.GetURL("api/v5/load_rebalance/" + nodeName + "/evacuation/start")
	resp, respBody, err := r.Request("POST", url, b, nil)
	if err != nil {
		return emperror.Wrap(err, "failed to request API api/v5/load_rebalance/"+nodeName+"/evacuation/start")
	}
	// TODO:
	// the api/v5/load_rebalance/global_status have some bugs, so we need to ignore the 400 error
	// wait for EMQX Dev Team fix it.
	if resp.StatusCode == 400 && strings.Contains(string(respBody), "already_started") {
		return nil
	}
	if resp.StatusCode != 200 {
		return emperror.Errorf("failed to request API %s, status : %s, body: %s", url.String(), resp.Status, respBody)
	}
	return nil
}
