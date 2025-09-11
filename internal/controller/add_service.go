package controller

import (
	"net/http"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
	req "github.com/emqx/emqx-operator/internal/requester"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type addService struct {
	*EMQXReconciler
}

func (a *addService) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	if r.api == nil {
		return subResult{}
	}

	if !instance.Status.IsConditionTrue(appsv2beta1.CoreNodesReady) {
		return subResult{}
	}

	configStr, err := a.getEMQXConfigsByAPI(r.api)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to get emqx configs by api")}
	}

	conf, err := config.EMQXConf(configStr)
	if err != nil {
		return subResult{err: emperror.Wrap(err, "failed to load emqx config")}
	}

	resources := []client.Object{}
	if dashboard := generateDashboardService(instance, conf); dashboard != nil {
		resources = append(resources, dashboard)
	}
	if listeners := generateListenerService(instance, conf); listeners != nil {
		resources = append(resources, listeners)
	}

	if err := a.CreateOrUpdateList(r.ctx, a.Scheme, r.log, instance, resources); err != nil {
		return subResult{err: emperror.Wrap(err, "failed to create or update services")}
	}
	return subResult{}
}

func (a *addService) getEMQXConfigsByAPI(req req.RequesterInterface) (string, error) {
	url := req.GetURL("api/v5/configs")

	resp, body, err := req.Request("GET", url, nil, http.Header{
		"Accept": []string{"text/plain"},
	})
	if err != nil {
		return "", emperror.Wrapf(err, "failed to get API %s", url.String())
	}
	if resp.StatusCode != 200 {
		return "", emperror.Errorf("failed to get API %s, status : %s, body: %s", url.String(), resp.Status, body)
	}
	return string(body), nil
}

func generateDashboardService(instance *appsv2beta1.EMQX, conf *config.Conf) *corev1.Service {
	svc := &corev1.Service{}
	if instance.Spec.DashboardServiceTemplate != nil {
		if !*instance.Spec.DashboardServiceTemplate.Enabled {
			return nil
		}
		svc.ObjectMeta = *instance.Spec.DashboardServiceTemplate.ObjectMeta.DeepCopy()
		svc.Spec = *instance.Spec.DashboardServiceTemplate.Spec.DeepCopy()
	}

	ports := conf.GetDashboardServicePort()
	if len(ports) == 0 {
		return nil
	}

	svc.Spec.Ports = appsv2beta1.MergeServicePorts(svc.Spec.Ports, ports)
	svc.Spec.Selector = appsv2beta1.DefaultCoreLabels(instance)

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   instance.Namespace,
			Name:        instance.DashboardServiceNamespacedName().Name,
			Labels:      appsv2beta1.CloneAndMergeMap(appsv2beta1.DefaultLabels(instance), svc.ObjectMeta.Labels),
			Annotations: svc.ObjectMeta.Annotations,
		},
		Spec: svc.Spec,
	}
}

func generateListenerService(instance *appsv2beta1.EMQX, conf *config.Conf) *corev1.Service {
	svc := &corev1.Service{}
	if instance.Spec.ListenersServiceTemplate != nil {
		if !*instance.Spec.ListenersServiceTemplate.Enabled {
			return nil
		}
		svc.ObjectMeta = *instance.Spec.ListenersServiceTemplate.ObjectMeta.DeepCopy()
		svc.Spec = *instance.Spec.ListenersServiceTemplate.Spec.DeepCopy()
	}

	ports := conf.GetListenersServicePorts()
	if len(ports) == 0 {
		ports = append(ports, []corev1.ServicePort{
			{
				Name:       "tcp-default",
				Protocol:   corev1.ProtocolTCP,
				Port:       1883,
				TargetPort: intstr.FromInt(1883),
			},
			{
				Name:       "ssl-default",
				Protocol:   corev1.ProtocolTCP,
				Port:       8883,
				TargetPort: intstr.FromInt(8883),
			},
			{
				Name:       "ws-default",
				Protocol:   corev1.ProtocolTCP,
				Port:       8083,
				TargetPort: intstr.FromInt(8083),
			},
			{
				Name:       "wss-default",
				Protocol:   corev1.ProtocolTCP,
				Port:       8084,
				TargetPort: intstr.FromInt(8084),
			},
		}...)
	}

	svc.Spec.Ports = appsv2beta1.MergeServicePorts(
		svc.Spec.Ports,
		ports,
	)
	svc.Spec.Selector = appsv2beta1.DefaultCoreLabels(instance)
	if appsv2beta1.IsExistReplicant(instance) && instance.Status.ReplicantNodesStatus.ReadyReplicas > 0 {
		svc.Spec.Selector = appsv2beta1.DefaultReplicantLabels(instance)
	}
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   instance.Namespace,
			Name:        instance.ListenersServiceNamespacedName().Name,
			Labels:      appsv2beta1.CloneAndMergeMap(appsv2beta1.DefaultLabels(instance), svc.ObjectMeta.Labels),
			Annotations: svc.ObjectMeta.Annotations,
		},
		Spec: svc.Spec,
	}
}
