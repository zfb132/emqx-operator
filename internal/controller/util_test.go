package controller

import (
	"testing"
	"time"

	crdv2 "github.com/emqx/emqx-operator/api/v2"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckInitialDelaySecondsReady(t *testing.T) {
	assert.False(t, checkInitialDelaySecondsReady(&crdv2.EMQX{}))

	assert.False(t, checkInitialDelaySecondsReady(&crdv2.EMQX{
		Spec: crdv2.EMQXSpec{
			UpdateStrategy: crdv2.UpdateStrategy{
				InitialDelaySeconds: 999999999,
			},
		},
		Status: crdv2.EMQXStatus{
			Conditions: []metav1.Condition{
				{
					Type:               crdv2.Available,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now()},
				},
			},
		},
	}))

	assert.True(t, checkInitialDelaySecondsReady(&crdv2.EMQX{
		Spec: crdv2.EMQXSpec{
			UpdateStrategy: crdv2.UpdateStrategy{
				InitialDelaySeconds: 0,
			},
		},
		Status: crdv2.EMQXStatus{
			Conditions: []metav1.Condition{
				{
					Type:               crdv2.Available,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
				},
			},
		},
	}))
}
