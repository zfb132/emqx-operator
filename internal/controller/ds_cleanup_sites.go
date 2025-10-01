package controller

import (
	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/emqx/emqx-operator/internal/emqx/api"
)

// Responsibilities:
// - Forget DS sites that are considered lost and do not have any shards.
type dsCleanupSites struct {
	*EMQXReconciler
}

func (c *dsCleanupSites) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// Instantiate API requester for a node that is part of update StatefulSet.
	// Required API operation is available only since EMQX 6.0.0.
	req := r.requester.forOldestCore(r.state, &emqxVersionFilter{instance: instance, prefix: "6."})

	// If there's no suitable EMQX API to query, skip the reconciliation.
	if req == nil {
		return subResult{}
	}

	// If EMQX DS API is not available, skip this reconciliation step.
	// We need this API to be available to ask it about replication status.
	cluster, err := api.GetDSCluster(req)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to fetch DS cluster status")}
	}

	for _, site := range cluster.Sites {
		if site.Up || (len(site.Shards) > 0) {
			continue
		}
		node := instance.Status.FindNode(site.Node)
		if node != nil {
			continue
		}
		err := api.ForgetDSSite(req, site.ID)
		if err != nil {
			return subResult{err: emperror.Wrapf(err, "failed to forget DS site %s", site.ID)}
		}
	}

	return subResult{}
}
