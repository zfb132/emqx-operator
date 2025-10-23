package controller

import (
	emperror "emperror.dev/errors"
	crdv2 "github.com/emqx/emqx-operator/api/v2"
	config "github.com/emqx/emqx-operator/internal/controller/config"
)

type loadConfig struct {
	*EMQXReconciler
}

func (l *loadConfig) reconcile(r *reconcileRound, instance *crdv2.EMQX) subResult {
	conf, err := config.EMQXConfigWithDefaults(applicableConfig(instance))
	if err != nil {
		return subResult{
			err: emperror.Wrap(err, "the .spec.config.data is not a valid HOCON config"),
		}
	}
	r.conf = conf
	return subResult{}
}
