/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"reflect"
	"time"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	innerErr "github.com/emqx/emqx-operator/internal/errors"
	"github.com/emqx/emqx-operator/internal/handler"
	req "github.com/emqx/emqx-operator/internal/requester"
)

// Currently executing round of reconciliation
type reconcileRound struct {
	ctx  context.Context
	log  logr.Logger
	conf *config.Conf
	// Populated by setupAPIRequester reconciler:
	api req.RequesterInterface
	// Populated by loadState reconciler:
	state *reconcileState
}

// subResult provides a wrapper around different results from a subreconciler.
type subResult struct {
	err    error
	result ctrl.Result
}

type subReconciler interface {
	reconcile(*reconcileRound, *appsv2beta1.EMQX) subResult
}

// EMQXReconciler reconciles a EMQX object
type EMQXReconciler struct {
	*handler.Handler
	Clientset     *kubernetes.Clientset
	Scheme        *runtime.Scheme
	EventRecorder record.EventRecorder
}

func NewEMQXReconciler(mgr manager.Manager) *EMQXReconciler {
	return &EMQXReconciler{
		Handler:       handler.NewHandler(mgr),
		Clientset:     kubernetes.NewForConfigOrDie(mgr.GetConfig()),
		Scheme:        mgr.GetScheme(),
		EventRecorder: mgr.GetEventRecorderFor("emqx-controller"),
	}
}

// +kubebuilder:rbac:groups=apps.emqx.io,resources=emqxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.emqx.io,resources=emqxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.emqx.io,resources=emqxes/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the EMQX object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *EMQXReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	instance := &appsv2beta1.EMQX{}
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if k8sErrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if instance.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)
	round := reconcileRound{ctx: ctx, log: logger}

	for _, subReconciler := range []subReconciler{
		&loadConfig{r},
		&loadState{r},
		&addBootstrap{r},
		&setupAPIRequester{r},
		&updateStatus{r},
		&updatePodConditions{r},
		&syncConfig{r},
		&addHeadlessSvc{r},
		&addCore{r},
		&addRepl{r},
		&addPdb{r},
		&addSvc{r},
		&dsUpdateReplicaSets{r},
		&dsReflectPodCondition{r},
		&syncPods{r},
		&syncSets{r},
	} {
		subResult := subReconciler.reconcile(&round, instance)
		if !subResult.result.IsZero() {
			return subResult.result, nil
		}
		if subResult.err != nil {
			if innerErr.IsCommonError(subResult.err) {
				logger.V(1).Info("requeue reconcile", "reconciler", subReconciler, "reason", subResult.err)
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning,
				"ReconcilerFailed", "reconcile failed at step %s, reason: %s",
				reflect.TypeOf(subReconciler).Elem().Name(),
				subResult.err.Error(),
			)
			return ctrl.Result{}, subResult.err
		}
	}

	isStable := instance.Status.IsConditionTrue(appsv2beta1.Ready) && instance.Status.DSReplication.IsStable()
	if !isStable {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: time.Duration(30) * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EMQXReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv2beta1.EMQX{}).
		Named("emqx").
		Complete(r)
}
