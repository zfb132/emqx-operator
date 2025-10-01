package controller

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/emqx/emqx-operator/internal/emqx/api"
)

type dsUpdateReplicaSets struct {
	*EMQXReconciler
}

func (u *dsUpdateReplicaSets) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// Get the most recent stateful set.
	updateCoreSet := r.state.updateCoreSet(instance)
	if updateCoreSet == nil {
		return subResult{}
	}

	// Instantiate API requester for a node that is part of update StatefulSet.
	req := r.requester.forOldestCore(r.state, &managedByFilter{r.state.updateCoreSet(instance)})

	// If there's no EMQX API to query, skip the reconciliation.
	if req == nil {
		return subResult{}
	}

	// Wait until all pods are ready.
	desiredReplicas := instance.Status.CoreNodesStatus.Replicas
	if updateCoreSet.Status.AvailableReplicas < desiredReplicas {
		return subResult{}
	}

	// Fetch the DS cluster info.
	// If EMQX DS API is not available, skip this reconciliation step.
	cluster, err := api.GetDSCluster(req)
	if err != nil && emperror.Is(err, api.ErrorNotFound) {
		return subResult{}
	}
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS cluster status")}
	}

	// Fetch the DS replication status.
	replication, err := api.GetDSReplicationStatus(req)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS replication status")}
	}

	// Compute the current sites.
	currentSites := replication.TargetSites()

	// Compute the target sites.
	targetSites := []string{}
	for _, node := range instance.Status.CoreNodes {
		pod := r.state.podWithName(node.PodName)
		if pod != nil && r.state.partOfUpdateSet(pod, instance) {
			site := cluster.FindSite(node.Node)
			if site == nil {
				return subResult{err: emperror.Wrapf(err, "no site for node %s", node.Node)}
			}
			if getPodIndex(node.PodName) < desiredReplicas {
				targetSites = append(targetSites, site.ID)
			}
		}
	}

	sort.Strings(targetSites)
	sort.Strings(currentSites)

	// Target sites are the same as current sites, no need to update.
	if reflect.DeepEqual(targetSites, currentSites) {
		return subResult{}
	}

	// Update replica sets for each DB.
	if len(replication.DBs) > 0 {
		r.log.V(1).Info("updating DS replica sets", "targetSites", targetSites, "currentSites", currentSites)
	}
	for _, db := range replication.DBs {
		err := api.UpdateDSReplicaSet(req, db.Name, targetSites)
		if err != nil {
			return subResult{err: emperror.Wrapf(err, "failed to update DB %s replica set", db.Name)}
		}
	}

	return subResult{}
}

func getPodIndex(podName string) int32 {
	parts := strings.Split(podName, "-")
	if len(parts) < 2 {
		return -1
	}
	indexPart := parts[len(parts)-1]
	index, err := strconv.Atoi(indexPart)
	if err != nil {
		return -1
	}
	return int32(index)
}
