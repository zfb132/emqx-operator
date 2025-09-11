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
	// If there's no EMQX API to query, skip the reconciliation.
	if r.api == nil {
		return subResult{}
	}

	// If EMQX DS is not enabled, skip this reconciliation step.
	if !r.conf.IsDSEnabled() {
		return subResult{}
	}

	// Get the most recent stateful set.
	updateCoreSet := r.state.updateCoreSet(instance)
	if updateCoreSet == nil {
		return subResult{}
	}

	// Wait until all pods are ready.
	desiredReplicas := instance.Status.CoreNodesStatus.Replicas
	if updateCoreSet.Status.AvailableReplicas < desiredReplicas {
		return subResult{}
	}

	// Fetch the DS cluster info.
	// If EMQX DS API is not available, skip this reconciliation step.
	cluster, err := api.GetDSCluster(r.api)
	if err != nil && emperror.Is(err, api.ErrorNotFound) {
		return subResult{}
	}
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS cluster status")}
	}

	// Fetch the DS replication status.
	replication, err := api.GetDSReplicationStatus(r.api)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS replication status")}
	}

	// Compute the current sites.
	currentSites := replication.TargetSites()

	// Compute the target sites.
	targetSites := []string{}
	for _, node := range instance.Status.CoreNodes {
		pod := r.state.podWithName(node.PodName)
		if r.state.partOfUpdateSet(pod, instance) {
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
	for _, db := range replication.DBs {
		err := api.UpdateDSReplicaSet(r.api, db.Name, targetSites)
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
