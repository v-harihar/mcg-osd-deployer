/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	noobaa "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	promv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	promv1a1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1alpha1"
	mcgv1alpha1 "github.com/red-hat-storage/mcg-osd-deployer/api/v1alpha1"
	"github.com/red-hat-storage/mcg-osd-deployer/templates"
	"github.com/red-hat-storage/mcg-osd-deployer/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ManagedMCGFinalizer = "managedmcg.openshift.io"
	NoobaaFinalizer     = "noobaa.io/graceful_finalizer"

	odfOperatorManagerconfigMapName = "odf-operator-manager-config"
	noobaaName                      = "noobaa"
	ManagedMCGName                  = "managedmcg"
	deployerCSVPrefix               = "mcg-osd-deployer"
	noobaaOperatorName              = "noobaa-operator"
)

// ImageMap holds mapping information between component image name and the image url
type ImageMap struct {
	NooBaaCore string
	NooBaaDB   string
}

// ManagedMCGReconciler reconciles a ManagedMCG object
type ManagedMCGReconciler struct {
	Client             client.Client
	UnrestrictedClient client.Client
	Log                logr.Logger
	Scheme             *runtime.Scheme
	ctx                context.Context
	managedMCG         *mcgv1alpha1.ManagedMCG
	namespace          string
	reconcileStrategy  mcgv1alpha1.ReconcileStrategy

	noobaa *noobaa.NooBaa
	images ImageMap

	AddonConfigMapName           string
	AddonConfigMapDeleteLabelKey string
	AddonParamSecretName         string

	prometheus                    *promv1.Prometheus
	pagerdutySecret               *corev1.Secret
	deadMansSnitchSecret          *corev1.Secret
	smtpSecret                    *corev1.Secret
	alertmanagerConfig            *promv1a1.AlertmanagerConfig
	alertRelabelConfigSecret      *corev1.Secret
	addonParams                   map[string]string
	onboardingValidationKeySecret *corev1.Secret
	alertmanager                  *promv1.Alertmanager
	PagerdutySecretName           string
	dmsRule                       *promv1.PrometheusRule
	DeadMansSnitchSecretName      string
	CustomerNotificationHTMLPath  string
	SMTPSecretName                string
	SOPEndpoint                   string
	AlertSMTPFrom                 string
	Route                         *routev1.Route
}

func (r *ManagedMCGReconciler) initReconciler(req ctrl.Request) {
	r.ctx = context.Background()
	r.namespace = req.NamespacedName.Namespace

	r.managedMCG = &mcgv1alpha1.ManagedMCG{}
	r.managedMCG.Name = req.NamespacedName.Name
	r.managedMCG.Namespace = r.namespace

	r.noobaa = &noobaa.NooBaa{}
	r.noobaa.Name = noobaaName
	r.noobaa.Namespace = r.namespace
	r.initPrometheusReconciler(req)

}

//+kubebuilder:rbac:groups=mcg.openshift.io,resources={managedmcg,managedmcg/finalizers},verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mcg.openshift.io,resources=managedmcg/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=mcg.openshift.io,resources={managedmcgs,managedmcgs/finalizers},verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mcg.openshift.io,resources=managedmcgs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",namespace=system,resources=configmaps,verbs=create;get;list;watch;update
//+kubebuilder:rbac:groups="coordination.k8s.io",namespace=system,resources=leases,verbs=create;get;list;watch;update
// +kubebuilder:rbac:groups=ocs.openshift.io,namespace=system,resources=storageclusters,verbs=get;list;watch;create;update

//+kubebuilder:rbac:groups=noobaa.io,namespace=system,resources=noobaas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",namespace=system,resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources={alertmanagers,prometheuses,alertmanagerconfigs},verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=prometheusrules,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=podmonitors,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=servicemonitors,verbs=get;list;watch;update;patch;create;delete
// +kubebuilder:rbac:groups="route.openshift.io",namespace=system,resources=routes,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="apps",namespace=system,resources=statefulsets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconcilication loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ManagedMCG object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *ManagedMCGReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("req.Namespace", req.Namespace, "req.Name", req.Name)
	log.Info("Starting reconcile for ManagedMCG.*")

	// Initalize the reconciler properties from the request
	r.initReconciler(req)

	if err := r.get(r.managedMCG); err != nil {
		if errors.IsNotFound(err) {
			r.Log.V(-1).Info("ManagedMCG resource is not found")
		} else {
			return ctrl.Result{}, err
		}
	}
	// Run the reconcile phases
	result, err := r.reconcilePhases()
	if err != nil {
		r.Log.Error(err, "An error was encountered during reconcilePhases")
	}

	// Ensure status is updated once even on failed reconciles
	var statusErr error
	if r.managedMCG.UID != "" {
		statusErr = r.Client.Status().Update(r.ctx, r.managedMCG)
	}

	// Reconcile errors have priority to status update errors
	if err != nil {
		return ctrl.Result{}, err
	} else if statusErr != nil {
		return ctrl.Result{}, statusErr
	} else {
		return result, nil
	}
}

func (r *ManagedMCGReconciler) reconcilePhases() (reconcile.Result, error) {
	r.Log.Info("Reconciler phase started..&&")

	initiateUninstall := r.checkUninstallCondition()
	// Update the status of the components
	r.updateComponentStatus()

	if !r.managedMCG.DeletionTimestamp.IsZero() {
		r.Log.Info("removing managedMCG resource if DeletionTimestamp exceeded")
		if r.verifyComponentsDoNotExist() {
			r.Log.Info("removing finalizer from the ManagedMCG resource")

			r.managedMCG.SetFinalizers(utils.Remove(r.managedMCG.Finalizers, ManagedMCGFinalizer))
			if err := r.Client.Update(r.ctx, r.managedMCG); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from ManagedMCG: %v", err)
			}
			r.Log.Info("finallizer removed successfully")

		} else {

			// noobaa needs to be deleted
			r.Log.Info("deleting noobaa")
			r.noobaa.SetFinalizers(utils.Remove(r.noobaa.GetFinalizers(), NoobaaFinalizer))
			if err := r.Client.Update(r.ctx, r.noobaa); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from Noobaa: %v", err)
			}

			if err := r.delete(r.noobaa); err != nil {
				return ctrl.Result{}, fmt.Errorf("unable to delete nooba: %v", err)
			}
		}

	} else if r.managedMCG.UID != "" {
		r.Log.Info("Reconciler phase started with valid UID")
		if !utils.Contains(r.managedMCG.GetFinalizers(), ManagedMCGFinalizer) {
			r.Log.V(-1).Info("finalizer missing on the managedMCG resource, adding...")
			r.managedMCG.SetFinalizers(append(r.managedMCG.GetFinalizers(), ManagedMCGFinalizer))
			if err := r.update(r.managedMCG); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update managedMCG with finalizer: %v", err)
			}
		}

		// Find the effective reconcile strategy
		r.reconcileStrategy = mcgv1alpha1.ReconcileStrategyStrict
		if strings.EqualFold(string(r.managedMCG.Spec.ReconcileStrategy), string(mcgv1alpha1.ReconcileStrategyNone)) {
			r.reconcileStrategy = mcgv1alpha1.ReconcileStrategyNone
		}

		// Read the add-on parameters secret and store it an addonParams map
		addonParamSecret := &corev1.Secret{}
		addonParamSecret.Name = r.AddonParamSecretName
		addonParamSecret.Namespace = r.namespace
		if err := r.get(addonParamSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("Failed to get the addon parameters secret %v", r.AddonParamSecretName)
		}
		for key, value := range addonParamSecret.Data {
			r.addonParams[key] = string(value)
		}

		if err := r.reconcileNoobaa(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileAlertMonitoring(); err != nil {
			return ctrl.Result{}, err
		}
		r.managedMCG.Status.ReconcileStrategy = r.reconcileStrategy

		// Check if we need and can uninstall
		if initiateUninstall && r.areComponentsReadyForUninstall() {

			r.Log.Info("starting MCG uninstallation - deleting managedMCG")
			if err := r.delete(r.managedMCG); err != nil {
				return ctrl.Result{}, fmt.Errorf("unable to delete managedMCG: %v", err)
			}

			if err := r.get(r.managedMCG); err != nil {
				if !errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
				r.Log.V(-1).Info("Trying to reload managedMCG resource after delete failed, managedMCG resource not found")
			}
		}

	} else if initiateUninstall {
		return ctrl.Result{}, r.removeOLMComponents()
	}
	return ctrl.Result{}, nil
}

func (r *ManagedMCGReconciler) removeOLMComponents() error {

	r.Log.Info("deleting deployer csv")
	if csv, err := r.getCSVByPrefix(deployerCSVPrefix); err == nil {
		if err := r.delete(csv); err != nil {
			return fmt.Errorf("Unable to delete csv: %v", err)
		} else {
			r.Log.Info("Deployer csv removed successfully")
			return nil
		}
	} else {
		return err
	}
}

func (r *ManagedMCGReconciler) areComponentsReadyForUninstall() bool {
	subComponents := r.managedMCG.Status.Components
	return subComponents.Noobaa.State == mcgv1alpha1.ComponentReady &&
		subComponents.Prometheus.State == mcgv1alpha1.ComponentReady &&
		subComponents.Alertmanager.State == mcgv1alpha1.ComponentReady
}

func (r *ManagedMCGReconciler) checkUninstallCondition() bool {
	configmap := &corev1.ConfigMap{}
	configmap.Name = r.AddonConfigMapName
	configmap.Namespace = r.namespace

	err := r.get(configmap)
	if err != nil {
		if !errors.IsNotFound(err) {
			r.Log.Error(err, "Unable to get addon delete configmap")
		}
		return false
	}
	_, ok := configmap.Labels[r.AddonConfigMapDeleteLabelKey]
	return ok
}

func (r *ManagedMCGReconciler) reconcileNoobaa() error {
	r.Log.Info("Reconciling Noobaa")
	noobaaList := noobaa.NooBaaList{}
	if err := r.list(&noobaaList); err == nil {
		for _, noobaa := range noobaaList.Items {
			if noobaa.Name == noobaaName {
				r.Log.Info("Noobaa instnce already exists.")
				return nil
			}
		}
	}
	desiredNoobaa := templates.NoobaTemplate.DeepCopy()
	err := r.setNooBaaDesiredState(desiredNoobaa)
	_, err = ctrl.CreateOrUpdate(r.ctx, r.Client, r.noobaa, func() error {
		r.noobaa.Spec = desiredNoobaa.Spec
		return nil
	})
	return err
}

func (r *ManagedMCGReconciler) setNooBaaDesiredState(desiredNoobaa *noobaa.NooBaa) error {
	coreResources := utils.GetResourceRequirements("noobaa-core")
	dbResources := utils.GetResourceRequirements("noobaa-db")
	dBVolumeResources := utils.GetResourceRequirements("noobaa-db-vol")
	endpointResources := utils.GetResourceRequirements("noobaa-endpoint")

	desiredNoobaa.Labels = map[string]string{
		"app": "noobaa",
	}
	desiredNoobaa.Spec.CoreResources = &coreResources
	desiredNoobaa.Spec.DBResources = &dbResources

	desiredNoobaa.Spec.DBVolumeResources = &dBVolumeResources
	desiredNoobaa.Spec.Image = &r.images.NooBaaCore
	desiredNoobaa.Spec.DBImage = &r.images.NooBaaDB
	desiredNoobaa.Spec.DBType = noobaa.DBTypePostgres

	// Default endpoint spec.
	desiredNoobaa.Spec.Endpoints = &noobaa.EndpointsSpec{
		MinCount:               1,
		MaxCount:               2,
		AdditionalVirtualHosts: []string{},
		Resources:              &endpointResources,
	}

	return nil
}

func (r *ManagedMCGReconciler) verifyComponentsDoNotExist() bool {
	subComponent := r.managedMCG.Status.Components

	if subComponent.Noobaa.State == mcgv1alpha1.ComponentNotFound {
		return true
	}
	return false
}

func (r *ManagedMCGReconciler) updateComponentStatus() {
	r.Log.Info("Updating component status")
	// Getting the status of the Noobaa component.
	noobaa := &r.managedMCG.Status.Components.Noobaa
	if err := r.get(r.noobaa); err == nil {
		if r.noobaa.Status.Phase == "Ready" {
			noobaa.State = mcgv1alpha1.ComponentReady
		} else {
			noobaa.State = mcgv1alpha1.ComponentPending
		}
	} else if errors.IsNotFound(err) {
		noobaa.State = mcgv1alpha1.ComponentNotFound
	} else {
		r.Log.V(-1).Info("error getting Noobaa, setting compoment status to Unknown")
		noobaa.State = mcgv1alpha1.ComponentUnknown
	}

	// Getting the status of the Prometheus component.
	promStatus := &r.managedMCG.Status.Components.Prometheus
	if err := r.get(r.prometheus); err == nil {
		promStatefulSet := &appsv1.StatefulSet{}
		promStatefulSet.Namespace = r.namespace
		promStatefulSet.Name = fmt.Sprintf("prometheus-%s", prometheusName)
		if err := r.get(promStatefulSet); err == nil {
			desiredReplicas := int32(1)
			if r.prometheus.Spec.Replicas != nil {
				desiredReplicas = *r.prometheus.Spec.Replicas
			}
			if promStatefulSet.Status.ReadyReplicas != desiredReplicas {
				promStatus.State = mcgv1alpha1.ComponentPending
			} else {
				promStatus.State = mcgv1alpha1.ComponentReady
			}
		} else {
			promStatus.State = mcgv1alpha1.ComponentPending
		}
	} else if errors.IsNotFound(err) {
		promStatus.State = mcgv1alpha1.ComponentNotFound
	} else {
		r.Log.V(-1).Info("error getting Prometheus, setting compoment status to Unknown")
		promStatus.State = mcgv1alpha1.ComponentUnknown
	}

	// Getting the status of the Alertmanager component.
	amStatus := &r.managedMCG.Status.Components.Alertmanager
	if err := r.get(r.alertmanager); err == nil {
		amStatefulSet := &appsv1.StatefulSet{}
		amStatefulSet.Namespace = r.namespace
		amStatefulSet.Name = fmt.Sprintf("alertmanager-%s", alertmanagerName)
		if err := r.get(amStatefulSet); err == nil {
			desiredReplicas := int32(1)
			if r.alertmanager.Spec.Replicas != nil {
				desiredReplicas = *r.alertmanager.Spec.Replicas
			}
			if amStatefulSet.Status.ReadyReplicas != desiredReplicas {
				amStatus.State = mcgv1alpha1.ComponentPending
			} else {
				amStatus.State = mcgv1alpha1.ComponentReady
			}
		} else {
			amStatus.State = mcgv1alpha1.ComponentPending
		}
	} else if errors.IsNotFound(err) {
		amStatus.State = mcgv1alpha1.ComponentNotFound
	} else {
		r.Log.V(-1).Info("error getting Alertmanager, setting compoment status to Unknown")
		amStatus.State = mcgv1alpha1.ComponentUnknown
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedMCGReconciler) SetupWithManager(mgr ctrl.Manager) error {

	if err := r.initializeImageVars(); err != nil {
		return err
	}

	managedMCGredicates := builder.WithPredicates(
		predicate.GenerationChangedPredicate{},
	)
	ctrlOptions := controller.Options{
		MaxConcurrentReconciles: 1,
	}
	enqueueManangedMCGRequest := handler.EnqueueRequestsFromMapFunc(
		func(client client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name:      ManagedMCGName,
					Namespace: client.GetNamespace(),
				},
			}}
		},
	)
	configMapPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				name := client.GetName()
				if name == odfOperatorManagerconfigMapName {
					return true
				}
				if name == alertmanagerConfigName {
					return true
				} else if name == r.AddonConfigMapName {
					if _, ok := client.GetLabels()[r.AddonConfigMapDeleteLabelKey]; ok {
						return true
					}
				}
				return false
			},
		),
	)

	prometheusRulesPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				labels := client.GetLabels()
				return labels == nil || labels[monLabelKey] != monLabelValue
			},
		),
	)

	secretPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				name := client.GetName()
				return name == r.AddonParamSecretName ||
					name == r.PagerdutySecretName ||
					name == r.DeadMansSnitchSecretName ||
					name == r.SMTPSecretName
			},
		),
	)

	noobaaPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return strings.HasPrefix(client.GetName(), noobaaName)
			},
		),
	)

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlOptions).
		For(&mcgv1alpha1.ManagedMCG{}, managedMCGredicates).

		// Watch owned resources
		Owns(&noobaa.NooBaa{}).

		// Watch non-owned resources
		Watches(
			&source.Kind{Type: &corev1.ConfigMap{}},
			enqueueManangedMCGRequest,
			configMapPredicates,
		).
		Watches(
			&source.Kind{Type: &noobaa.NooBaa{}},
			enqueueManangedMCGRequest,
			noobaaPredicates,
		).
		Watches(
			&source.Kind{Type: &corev1.Secret{}},
			enqueueManangedMCGRequest,
			secretPredicates,
		).
		Watches(
			&source.Kind{Type: &promv1.PrometheusRule{}},
			enqueueManangedMCGRequest,
			prometheusRulesPredicates,
		).
		Complete(r)
}

func (r *ManagedMCGReconciler) initializeImageVars() error {
	r.images.NooBaaCore = os.Getenv("NOOBAA_CORE_IMAGE")
	r.images.NooBaaDB = os.Getenv("NOOBAA_DB_IMAGE")

	if r.images.NooBaaCore == "" {
		err := fmt.Errorf("NOOBAA_CORE_IMAGE environment variable not found")
		r.Log.Error(err, "Missing NOOBAA_CORE_IMAGE environment variable for MCG initialization.")
		return err
	} else if r.images.NooBaaDB == "" {
		err := fmt.Errorf("NOOBAA_DB_IMAGE environment variable not found")
		r.Log.Error(err, "Missing NOOBAA_DB_IMAGE environment variable for MCG initialization.")
		return err
	}
	return nil
}

func (r *ManagedMCGReconciler) get(obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)

	return r.Client.Get(r.ctx, key, obj)
}

func (r *ManagedMCGReconciler) list(obj client.ObjectList) error {
	listOptions := client.InNamespace(r.namespace)
	return r.Client.List(r.ctx, obj, listOptions)
}

func (r *ManagedMCGReconciler) update(obj client.Object) error {
	return r.Client.Update(r.ctx, obj)
}

func (r *ManagedMCGReconciler) delete(obj client.Object) error {
	if err := r.Client.Delete(r.ctx, obj); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ManagedMCGReconciler) own(resource metav1.Object) error {
	// Ensure ManagedMCG ownership on a resource
	if err := ctrl.SetControllerReference(r.managedMCG, resource, r.Scheme); err != nil {
		return err
	}
	return nil
}

func (r *ManagedMCGReconciler) getCSVByPrefix(name string) (*opv1a1.ClusterServiceVersion, error) {
	csvList := opv1a1.ClusterServiceVersionList{}
	if err := r.list(&csvList); err != nil {
		return nil, fmt.Errorf("unable to list csv resources: %v", err)
	}
	var csv *opv1a1.ClusterServiceVersion = nil
	for _, candidate := range csvList.Items {
		if strings.HasPrefix(candidate.Name, name) {
			csv = &candidate
			break
		}
	}
	if csv == nil {
		return nil, fmt.Errorf("unable to get csv resources for %s ", name)
	}
	return csv, nil
}

func (r *ManagedMCGReconciler) unrestrictedGet(obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	return r.UnrestrictedClient.Get(r.ctx, key, obj)
}
