package controller

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
	req "github.com/emqx/emqx-operator/internal/requester"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	k8s "sigs.k8s.io/controller-runtime/pkg/client"
)

type setupAPIRequester struct {
	*EMQXReconciler
}

func (l *setupAPIRequester) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	req, err := apiRequester(r.ctx, l.Client, r.conf, r.state, instance)
	if err != nil {
		return subResult{err: err}
	}
	r.api = req
	return subResult{}
}

func apiRequester(
	ctx context.Context,
	client k8s.Client,
	conf *config.Conf,
	state *reconcileState,
	instance *appsv2beta1.EMQX,
) (req.RequesterInterface, error) {
	username, password, err := getBootstrapAPIKey(ctx, client, instance.BootstrapAPIKeyNamespacedName())
	if err != nil {
		return nil, emperror.Wrap(err, "failed to get bootstrap API key")
	}

	var schema, port string
	portMap := conf.GetDashboardPortMap()
	if dashboardHttps, ok := portMap["dashboard-https"]; ok {
		schema = "https"
		port = strconv.Itoa(dashboardHttps)
	}
	if dashboard, ok := portMap["dashboard"]; ok {
		schema = "http"
		port = strconv.Itoa(dashboard)
	}

	corePods := state.podsWithRole("core")
	sort.Slice(corePods, func(i, j int) bool {
		return corePods[i].CreationTimestamp.Before(&corePods[j].CreationTimestamp)
	})
	for _, pod := range corePods {
		if pod.Status.PodIP != "" {
			cond := appsv2beta1.FindPodCondition(pod, corev1.ContainersReady)
			if cond != nil && cond.Status == corev1.ConditionTrue {
				req := &req.Requester{
					Schema:   schema,
					Host:     net.JoinHostPort(pod.Status.PodIP, port),
					Username: username,
					Password: password,
				}
				return req, nil
			}
		}
	}

	return nil, nil
}

func getBootstrapAPIKey(ctx context.Context, client k8s.Client, name types.NamespacedName) (username, password string, err error) {
	bootstrapAPIKey := &corev1.Secret{}
	if err = client.Get(ctx, name, bootstrapAPIKey); err != nil {
		err = emperror.Wrap(err, "get secret failed")
		return
	}

	if data, ok := bootstrapAPIKey.Data["bootstrap_api_key"]; ok {
		users := strings.Split(string(data), "\n")
		for _, user := range users {
			index := strings.Index(user, ":")
			if index > 0 && user[:index] == appsv2beta1.DefaultBootstrapAPIKey {
				username = user[:index]
				password = user[index+1:]
				return
			}
		}
	}

	err = emperror.Errorf("the secret does not contain the bootstrap_api_key")
	return
}
