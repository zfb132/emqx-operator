package controller

import (
	"fmt"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
	resources "github.com/emqx/emqx-operator/internal/controller/resources"
	"github.com/emqx/emqx-operator/internal/emqx/api"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

type syncConfig struct {
	*EMQXReconciler
}

func (s *syncConfig) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// Make sure the config map exists
	resource := resources.EMQXConfig(instance)
	configMap := &corev1.ConfigMap{}
	err := s.Client.Get(r.ctx, resource.ConfigsNamespacedName(), configMap)
	if err != nil && k8sErrors.IsNotFound(err) {
		configMap = resource.ConfigMap()
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
	if resource.DiffersFrom(configMap) {
		configMap = resource.ConfigMap()
		r.log.V(1).Info("updating config resource", "configMap", klog.KObj(configMap))
		if err := s.Client.Update(r.ctx, configMap); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update configMap")}
		}
	}

	confStr := resource.BaseConfig()
	lastConfStr, ok := instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey]

	// If the annotation is not set, set it to the current config and return.
	// This reconciler is apparently running for the first time.
	if !ok {
		if instance.Annotations == nil {
			instance.Annotations = map[string]string{}
		}
		instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey] = confStr
		if err := s.Client.Update(r.ctx, instance); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx instance annotation")}
		}
		return subResult{}
	}

	// If the annotation is set, and the config is different, update the config.
	if lastConfStr != confStr {
		if !instance.Status.IsConditionTrue(appsv2beta1.CoreNodesReady) {
			return subResult{}
		}

		// Delete readonly configs
		conf, err := config.EMQXConfig(confStr)
		if err != nil {
			return subResult{err: emperror.Wrap(err, "failed to parse .spec.config.data")}
		}
		stripped := conf.StripReadOnlyConfig()
		if len(stripped) > 0 {
			s.EventRecorder.Event(
				instance,
				corev1.EventTypeNormal, "WontUpdateReadOnlyConfig",
				fmt.Sprintf("Stripped readonly config entries, will not be updated: %v", stripped),
			)
		}

		r.log.V(1).Info("applying runtime config", "config", conf.Print())
		if err := api.UpdateConfigs(r.oldestCoreRequester(), instance.Spec.Config.Mode, conf.Print()); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx config through API")}
		}

		instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey] = confStr
		if err := s.Client.Update(r.ctx, instance); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update emqx instance annotation")}
		}
	}

	return subResult{}
}

func applicableConfig(instance *appsv2beta1.EMQX) string {
	// If the annotation is set, use it: most of the time it's the config currently in use.
	if instance.Annotations != nil {
		if config := instance.Annotations[appsv2beta1.AnnotationsLastEMQXConfigKey]; config != "" {
			return config
		}
	}
	// If not, running for the first time, use spec's config.
	return instance.Spec.Config.Data
}
