package controller

import (
	"fmt"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	resources "github.com/emqx/emqx-operator/internal/controller/resources"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

type syncConfig struct {
	*EMQXReconciler
}

func (s *syncConfig) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// Make sure the config map exists
	configMap := &corev1.ConfigMap{}
	err := s.Client.Get(r.ctx, instance.ConfigsNamespacedName(), configMap)
	if err != nil && k8sErrors.IsNotFound(err) {
		configMap = resources.EMQXConfig(instance).ConfigMap()
		if err := ctrl.SetControllerReference(instance, configMap, s.Scheme); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to set controller reference for configMap")}
		}
		if err := s.Client.Create(r.ctx, configMap); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to create configMap")}
		}
		return subResult{}
	}
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to get configMap")}
	}

	// If the config is different, update the config right away.
	// Assuming the config is valid, otherwise master controller would bail out.
	if configMap.Data[resources.BaseConfigFile] != instance.Spec.Config.Data {
		configMap = resources.EMQXConfig(instance).ConfigMap()
		if err := s.Client.Update(r.ctx, configMap); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update configMap")}
		}
	}

	lastConfStr, ok := instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey]

	// If the annotation is not set, set it to the current config and return.
	if !ok {
		if instance.Annotations == nil {
			instance.Annotations = map[string]string{}
		}
		instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey] = instance.Spec.Config.Data
		if err := s.Client.Update(r.ctx, instance); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx instance annotation")}
		}
		return subResult{}
	}

	// If the annotation is set, and the config is different, update the config.
	if lastConfStr != instance.Spec.Config.Data {
		if !instance.Status.IsConditionTrue(appsv2beta1.CoreNodesReady) {
			return subResult{}
		}

		// Delete readonly configs
		conf := r.conf.Copy()
		stripped := conf.StripReadOnlyConfig()
		if len(stripped) > 0 {
			s.EventRecorder.Event(
				instance,
				corev1.EventTypeNormal, "WontUpdateReadOnlyConfig",
				fmt.Sprintf("Stripped readonly config entries, will not be updated: %v", stripped),
			)
		}

		if err := api.UpdateConfigs(r.oldestCoreRequester(), instance.Spec.Config.Mode, conf.Print()); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx config through API")}
		}

		instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey] = instance.Spec.Config.Data
		if err := s.Client.Update(r.ctx, instance); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx instance annotation")}
		}
	}

	return subResult{}
}
