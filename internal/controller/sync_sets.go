package controller

import (
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type syncSets struct {
	*EMQXReconciler
}

func (s *syncSets) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	if !instance.Status.IsConditionTrue(appsv2beta1.Ready) {
		return subResult{}
	}

	currentRs := r.state.currentReplicantSet(instance)
	updateRs := r.state.updateReplicantSet(instance)
	oldRsList := []*appsv1.ReplicaSet{}
	for _, rs := range r.state.replicantSets {
		if rs != currentRs && rs != updateRs {
			oldRsList = append(oldRsList, rs.DeepCopy())
		}
	}

	rsDiff := int32(len(oldRsList)) - *instance.Spec.RevisionHistoryLimit
	if rsDiff > 0 {
		for i := 0; i < int(rsDiff); i++ {
			rs := oldRsList[i].DeepCopy()
			// Avoid delete replica set with non-zero replica counts
			if rs.Status.Replicas != 0 || *(rs.Spec.Replicas) != 0 || rs.Generation > rs.Status.ObservedGeneration || rs.DeletionTimestamp != nil {
				continue
			}
			r.log.Info("trying to cleanup replicaSet for EMQX", "replicaSet", klog.KObj(rs), "EMQX", klog.KObj(instance))
			if err := s.Client.Delete(r.ctx, rs); err != nil && !k8sErrors.IsNotFound(err) {
				return subResult{err: err}
			}
		}
	}

	currentSts := r.state.currentCoreSet(instance)
	updateSts := r.state.updateCoreSet(instance)
	oldStsList := []*appsv1.StatefulSet{}
	for _, sts := range r.state.coreSets {
		if sts != currentSts && sts != updateSts {
			oldStsList = append(oldStsList, sts.DeepCopy())
		}
	}

	stsDiff := int32(len(oldStsList)) - *instance.Spec.RevisionHistoryLimit
	if stsDiff > 0 {
		for i := 0; i < int(stsDiff); i++ {
			sts := oldStsList[i].DeepCopy()
			// Avoid delete stateful set with non-zero replica counts
			if sts.Status.Replicas != 0 || *(sts.Spec.Replicas) != 0 || sts.Generation > sts.Status.ObservedGeneration || sts.DeletionTimestamp != nil {
				continue
			}
			r.log.Info("trying to cleanup statefulSet for EMQX", "statefulSet", klog.KObj(sts), "EMQX", klog.KObj(instance))
			if err := s.Client.Delete(r.ctx, sts); err != nil && !k8sErrors.IsNotFound(err) {
				return subResult{err: err}
			}

			// Delete PVCs
			pvcList := &corev1.PersistentVolumeClaimList{}
			_ = s.Client.List(r.ctx, pvcList,
				client.InNamespace(instance.Namespace),
				client.MatchingLabels(sts.Spec.Selector.MatchLabels),
			)

			for _, p := range pvcList.Items {
				pvc := p.DeepCopy()
				if pvc.DeletionTimestamp != nil {
					continue
				}
				r.log.Info("trying to cleanup persistentVolumeClaim for EMQX", "persistentVolumeClaim", klog.KObj(pvc), "EMQX", klog.KObj(instance))
				if err := s.Client.Delete(r.ctx, pvc); err != nil && !k8sErrors.IsNotFound(err) {
					return subResult{err: err}
				}
			}
		}
	}

	return subResult{}
}
