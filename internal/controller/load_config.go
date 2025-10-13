package controller

import (
	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
)

type loadConfig struct {
	*EMQXReconciler
}

func (l *loadConfig) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	conf, err := config.EMQXConf(instance.Spec.Config.Data)
	if err != nil {
		return subResult{
			err: emperror.Wrap(err, "the .spec.config.data is not a valid HOCON config"),
		}
	}
	r.conf = conf
	return subResult{}
}
