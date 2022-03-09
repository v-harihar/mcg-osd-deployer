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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	noobaa "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	operatorv1 "github.com/openshift/api/operator/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	promv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	promv1a1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1alpha1"
	odfv1alpha1 "github.com/red-hat-data-services/odf-operator/api/v1alpha1"
	mcgv1alpha1 "github.com/red-hat-storage/mcg-osd-deployer/api/v1alpha1"
	"github.com/red-hat-storage/mcg-osd-deployer/templates"
	"github.com/red-hat-storage/mcg-osd-deployer/utils"
	ocsv1 "github.com/red-hat-storage/ocs-operator/api/v1"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	storageSystemName               = "mcg-storagesystem"
	ManagedMCGName                  = "managedmcg"
	odfOperatorName                 = "odf-operator"
	operatorConsoleName             = "cluster"
	odfConsoleName                  = "odf-console"
	storageClusterName              = "mcg-storagecluster"
	deployerCSVPrefix               = "mcg-osd-deployer"
	noobaaOperatorName              = "noobaa-operator"

	alertRelabelConfigSecretKey            = "alertrelabelconfig.yaml"
	prometheusName                         = "managed-mcg-prometheus"
	monLabelKey                            = "app"
	monLabelValue                          = "managed-mcg"
	alertRelabelConfigSecretName           = "managed-mcg-alert-relabel-config-secret"
	alertmanagerName                       = "managed-mcg-alertmanager"
	notificationEmailKeyPrefix             = "notification-email"
	grafanaDatasourceSecretName            = "grafana-datasources"
	grafanaDatasourceSecretKey             = "prometheus.yaml"
	k8sMetricsServiceMonitorAuthSecretName = "k8s-metrics-service-monitor-auth"
	openshiftMonitoringNamespace           = "openshift-monitoring"
	dmsRuleName                            = "dms-monitor-rule"
	alertmanagerConfigName                 = "managed-mcg-alertmanager-config"
	k8sMetricsServiceMonitorName           = "k8s-metrics-service-monitor"
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

	odfOperatorManagerconfigMap *corev1.ConfigMap
	noobaa                      *noobaa.NooBaa
	storageSystem               *odfv1alpha1.StorageSystem
	images                      ImageMap
	operatorConsole             *operatorv1.Console
	storageCluster              *ocsv1.StorageCluster

	AddonConfigMapName           string
	AddonConfigMapDeleteLabelKey string

	prometheus                         *promv1.Prometheus
	pagerdutySecret                    *corev1.Secret
	deadMansSnitchSecret               *corev1.Secret
	smtpSecret                         *corev1.Secret
	alertmanagerConfig                 *promv1a1.AlertmanagerConfig
	alertRelabelConfigSecret           *corev1.Secret
	k8sMetricsServiceMonitor           *promv1.ServiceMonitor
	k8sMetricsServiceMonitorAuthSecret *corev1.Secret
	addonParams                        map[string]string
	onboardingValidationKeySecret      *corev1.Secret
	alertmanager                       *promv1.Alertmanager
	PagerdutySecretName                string
	SOPEndpoint                        string
	dmsRule                            *promv1.PrometheusRule
	DeadMansSnitchSecretName           string
}

func (r *ManagedMCGReconciler) initReconciler(req ctrl.Request) {
	r.ctx = context.Background()
	r.namespace = req.NamespacedName.Namespace

	r.managedMCG = &mcgv1alpha1.ManagedMCG{}
	r.managedMCG.Name = req.NamespacedName.Name
	r.managedMCG.Namespace = r.namespace

	r.odfOperatorManagerconfigMap = &corev1.ConfigMap{}
	r.odfOperatorManagerconfigMap.Name = odfOperatorManagerconfigMapName
	r.odfOperatorManagerconfigMap.Namespace = r.namespace
	r.odfOperatorManagerconfigMap.Data = make(map[string]string)

	r.noobaa = &noobaa.NooBaa{}
	r.noobaa.Name = noobaaName
	r.noobaa.Namespace = r.namespace

	r.storageSystem = &odfv1alpha1.StorageSystem{}
	r.storageSystem.Name = storageSystemName
	r.storageSystem.Namespace = r.namespace

	r.operatorConsole = &operatorv1.Console{}
	r.operatorConsole.Name = operatorConsoleName

	r.storageCluster = &ocsv1.StorageCluster{}
	r.storageCluster.Name = storageClusterName
	r.storageCluster.Namespace = r.namespace

	r.prometheus = &promv1.Prometheus{}
	r.prometheus.Name = prometheusName
	r.prometheus.Namespace = r.namespace

	r.alertmanager = &promv1.Alertmanager{}
	r.alertmanager.Name = alertmanagerName
	r.alertmanager.Namespace = r.namespace

	r.pagerdutySecret = &corev1.Secret{}
	r.pagerdutySecret.Name = r.PagerdutySecretName
	r.pagerdutySecret.Namespace = r.namespace

	r.dmsRule = &promv1.PrometheusRule{}
	r.dmsRule.Name = dmsRuleName
	r.dmsRule.Namespace = r.namespace

	r.deadMansSnitchSecret = &corev1.Secret{}
	r.deadMansSnitchSecret.Name = r.DeadMansSnitchSecretName
	r.deadMansSnitchSecret.Namespace = r.namespace

	r.alertmanagerConfig = &promv1a1.AlertmanagerConfig{}
	r.alertmanagerConfig.Name = alertmanagerConfigName
	r.alertmanagerConfig.Namespace = r.namespace

	r.k8sMetricsServiceMonitor = &promv1.ServiceMonitor{}
	r.k8sMetricsServiceMonitor.Name = k8sMetricsServiceMonitorName
	r.k8sMetricsServiceMonitor.Namespace = r.namespace

	r.k8sMetricsServiceMonitorAuthSecret = &corev1.Secret{}
	r.k8sMetricsServiceMonitorAuthSecret.Name = k8sMetricsServiceMonitorAuthSecretName
	r.k8sMetricsServiceMonitorAuthSecret.Namespace = r.namespace

	r.alertRelabelConfigSecret = &corev1.Secret{}
	r.alertRelabelConfigSecret.Name = alertRelabelConfigSecretName
	r.alertRelabelConfigSecret.Namespace = r.namespace

}

//+kubebuilder:rbac:groups=mcg.openshift.io,resources={managedmcg,managedmcg/finalizers},verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mcg.openshift.io,resources=managedmcg/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=mcg.openshift.io,resources={managedmcgs,managedmcgs/finalizers},verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mcg.openshift.io,resources=managedmcgs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",namespace=system,resources=configmaps,verbs=create;get;list;watch;update
//+kubebuilder:rbac:groups="coordination.k8s.io",namespace=system,resources=leases,verbs=create;get;list;watch;update
//+kubebuilder:rbac:groups=operators.coreos.com,namespace=system,resources=clusterserviceversions,verbs=get;list;watch;delete;update;patch
// +kubebuilder:rbac:groups=ocs.openshift.io,namespace=system,resources=storageclusters,verbs=get;list;watch;create;update

//+kubebuilder:rbac:groups=noobaa.io,namespace=system,resources=noobaas,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=odf.openshift.io,namespace=system,resources=storagesystems,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=console.openshift.io,resources=consoleplugins,verbs=*
//+kubebuilder:rbac:groups=operator.openshift.io,resources=consoles,verbs=*

// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources={alertmanagers,prometheuses,alertmanagerconfigs},verbs=get;list;watch;create;update;
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=prometheusrules,verbs=get;list;watch;create;update;
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=podmonitors,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="monitoring.coreos.com",namespace=system,resources=servicemonitors,verbs=get;list;watch;update;patch;create;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
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
			r.Log.V(-1).Info("ManagedMCG resource not found..")
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

			// Deleting all storage systems from the namespace
			r.Log.Info("deleting storageSystems")
			if err := r.delete(r.storageSystem); err != nil {
				return ctrl.Result{}, fmt.Errorf("unable to delete storageSystem: %v", err)
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

		// Reconcile the different resources
		//TODO : remove once ODF upgrad to 4.10
		if err := r.reconcileODFOperatorMgrConfig(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileConsoleCluster(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileStorageSystem(); err != nil {
			return ctrl.Result{}, err
		}
		//TODO : remove once ODF upgrad to 4.10
		if err := r.reconcileODFCSV(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileNoobaa(); err != nil {
			return ctrl.Result{}, err
		}
		//TODO : remove once ODF upgrad to 4.10
		if err := r.reconcileStorageCluster(); err != nil {
			return ctrl.Result{}, err
		}

		/*
			Alert start here
		*/
		if err := r.reconcileAlertRelabelConfigSecret(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcilePrometheus(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileAlertmanager(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileAlertmanagerConfig(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileK8SMetricsServiceMonitorAuthSecret(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileK8SMetricsServiceMonitor(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileMonitoringResources(); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileDMSPrometheusRule(); err != nil {
			return ctrl.Result{}, err
		}
		/*
			Alert End here
		*/
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
	return subComponents.Noobaa.State == mcgv1alpha1.ComponentReady
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

func (r *ManagedMCGReconciler) reconcileDMSPrometheusRule() error {
	r.Log.Info("Reconciling DMS Prometheus Rule")

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.dmsRule, func() error {
		if err := r.own(r.dmsRule); err != nil {
			return err
		}

		desired := templates.DMSPrometheusRuleTemplate.DeepCopy()

		for _, group := range desired.Spec.Groups {
			if group.Name == "snitch-alert" {
				for _, rule := range group.Rules {
					if rule.Alert == "DeadMansSnitch" {
						rule.Labels["namespace"] = r.namespace
					}
				}
			}
		}

		r.dmsRule.Spec = desired.Spec

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *ManagedMCGReconciler) reconcileK8SMetricsServiceMonitorAuthSecret() error {
	r.Log.Info("Reconciling k8sMetricsServiceMonitorAuthSecret")

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.k8sMetricsServiceMonitorAuthSecret, func() error {
		if err := r.own(r.k8sMetricsServiceMonitorAuthSecret); err != nil {
			return err
		}

		secret := &corev1.Secret{}
		secret.Name = grafanaDatasourceSecretName
		secret.Namespace = openshiftMonitoringNamespace
		if err := r.unrestrictedGet(secret); err != nil {
			return fmt.Errorf("Failed to get grafana-datasources secret from openshift-monitoring namespace: %v", err)
		}

		authInfoStructure := struct {
			DataSources []struct {
				BasicAuthPassword string `json:"basicAuthPassword"`
				BasicAuthUser     string `json:"basicAuthUser"`
			} `json:"datasources"`
		}{}

		if err := json.Unmarshal(secret.Data[grafanaDatasourceSecretKey], &authInfoStructure); err != nil {
			return fmt.Errorf("Could not unmarshal Grapana datasource data: %v", err)
		}

		r.k8sMetricsServiceMonitorAuthSecret.Data = nil
		for key := range authInfoStructure.DataSources {
			ds := &authInfoStructure.DataSources[key]
			if ds.BasicAuthUser == "internal" && ds.BasicAuthPassword != "" {
				r.k8sMetricsServiceMonitorAuthSecret.Data = map[string][]byte{
					"Username": []byte(ds.BasicAuthUser),
					"Password": []byte(ds.BasicAuthPassword),
				}
			}
		}
		if r.k8sMetricsServiceMonitorAuthSecret.Data == nil {
			return fmt.Errorf("Grapana datasource does not contain the needed credentials")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("Failed to update k8sMetricsServiceMonitorAuthSecret: %v", err)
	}
	return nil
}

func (r *ManagedMCGReconciler) reconcileK8SMetricsServiceMonitor() error {
	r.Log.Info("Reconciling k8sMetricsServiceMonitor")

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.k8sMetricsServiceMonitor, func() error {
		if err := r.own(r.k8sMetricsServiceMonitor); err != nil {
			return err
		}
		desired := templates.K8sMetricsServiceMonitorTemplate.DeepCopy()
		r.k8sMetricsServiceMonitor.Spec = desired.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("Failed to update k8sMetricsServiceMonitor: %v", err)
	}
	return nil
}

// reconcileMonitoringResources labels all monitoring resources (ServiceMonitors, PodMonitors, and PrometheusRules)
// found in the target namespace with a label that matches the label selector the defined on the Prometheus resource
// we are reconciling in reconcilePrometheus. Doing so instructs the Prometheus instance to notice and react to these labeled
// monitoring resources
func (r *ManagedMCGReconciler) reconcileMonitoringResources() error {
	r.Log.Info("reconciling monitoring resources")

	podMonitorList := promv1.PodMonitorList{}
	if err := r.list(&podMonitorList); err != nil {
		return fmt.Errorf("Could not list pod monitors: %v", err)
	}
	for i := range podMonitorList.Items {
		obj := podMonitorList.Items[i]
		utils.AddLabel(obj, monLabelKey, monLabelValue)
		if err := r.update(obj); err != nil {
			return err
		}
	}

	serviceMonitorList := promv1.ServiceMonitorList{}
	if err := r.list(&serviceMonitorList); err != nil {
		return fmt.Errorf("Could not list service monitors: %v", err)
	}
	for i := range serviceMonitorList.Items {
		obj := serviceMonitorList.Items[i]
		utils.AddLabel(obj, monLabelKey, monLabelValue)
		if err := r.update(obj); err != nil {
			return err
		}
	}

	promRuleList := promv1.PrometheusRuleList{}
	if err := r.list(&promRuleList); err != nil {
		return fmt.Errorf("Could not list prometheus rules: %v", err)
	}
	for i := range promRuleList.Items {
		obj := promRuleList.Items[i]
		utils.AddLabel(obj, monLabelKey, monLabelValue)
		if err := r.update(obj); err != nil {
			return err
		}
	}

	return nil
}

func (r *ManagedMCGReconciler) reconcileAlertmanagerConfig() error {
	r.Log.Info("Reconciling AlertmanagerConfig secret")

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.alertmanagerConfig, func() error {
		if err := r.own(r.alertmanagerConfig); err != nil {
			return err
		}

		if err := r.get(r.pagerdutySecret); err != nil {
			return fmt.Errorf("Unable to get pagerduty secret: %v", err)
		}
		pagerdutySecretData := r.pagerdutySecret.Data
		pagerdutyServiceKey := string(pagerdutySecretData["PAGERDUTY_KEY"])
		if pagerdutyServiceKey == "" {
			return fmt.Errorf("Pagerduty secret does not contain a PAGERDUTY_KEY entry")
		}

		if r.deadMansSnitchSecret.UID == "" {
			if err := r.get(r.deadMansSnitchSecret); err != nil {
				return fmt.Errorf("Unable to get DeadMan's Snitch secret: %v", err)
			}
		}
		dmsURL := string(r.deadMansSnitchSecret.Data["SNITCH_URL"])
		if dmsURL == "" {
			return fmt.Errorf("DeadMan's Snitch secret does not contain a SNITCH_URL entry")
		}

		alertingAddressList := []string{}
		i := 0
		for {
			alertingAddressKey := fmt.Sprintf("%s-%v", notificationEmailKeyPrefix, i)
			alertingAddress, found := r.addonParams[alertingAddressKey]
			i++
			if found {
				alertingAddressAsString := alertingAddress
				if alertingAddressAsString != "" {
					alertingAddressList = append(alertingAddressList, alertingAddressAsString)
				}
			} else {
				break
			}
		}

		desired := templates.AlertmanagerConfigTemplate.DeepCopy()
		for i := range desired.Spec.Receivers {
			receiver := &desired.Spec.Receivers[i]
			switch receiver.Name {
			case "pagerduty":
				receiver.PagerDutyConfigs[0].ServiceKey.Key = "PAGERDUTY_KEY"
				receiver.PagerDutyConfigs[0].ServiceKey.LocalObjectReference.Name = r.PagerdutySecretName
				receiver.PagerDutyConfigs[0].Details[0].Key = "SOP"
				receiver.PagerDutyConfigs[0].Details[0].Value = r.SOPEndpoint
			case "DeadMansSnitch":
				receiver.WebhookConfigs[0].URL = &dmsURL
			}
		}
		r.alertmanagerConfig.Spec = desired.Spec
		utils.AddLabel(r.alertmanagerConfig, monLabelKey, monLabelValue)

		return nil
	})

	return err
}

func (r *ManagedMCGReconciler) reconcileAlertmanager() error {
	r.Log.Info("Reconciling Alertmanager")
	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.alertmanager, func() error {
		if err := r.own(r.alertmanager); err != nil {
			return err
		}

		desired := templates.AlertmanagerTemplate.DeepCopy()
		desired.Spec.AlertmanagerConfigSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				monLabelKey: monLabelValue,
			},
		}
		r.alertmanager.Spec = desired.Spec
		utils.AddLabel(r.alertmanager, monLabelKey, monLabelValue)

		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *ManagedMCGReconciler) reconcilePrometheus() error {
	r.Log.Info("Reconciling Prometheus")
	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.prometheus, func() error {
		if err := r.own(r.prometheus); err != nil {
			return err
		}
		desired := templates.PrometheusTemplate.DeepCopy()
		r.prometheus.ObjectMeta.Labels = map[string]string{monLabelKey: monLabelValue}
		r.prometheus.Spec = desired.Spec
		r.prometheus.Spec.Alerting.Alertmanagers[0].Namespace = r.namespace
		r.prometheus.Spec.AdditionalAlertRelabelConfigs = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: alertRelabelConfigSecretName,
			},
			Key: alertRelabelConfigSecretKey,
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// AlertRelabelConfigSecret will have configuration for relabeling the alerts that are firing.
// It will add namespace label to firing alerts before they are sent to the alertmanager
func (r *ManagedMCGReconciler) reconcileAlertRelabelConfigSecret() error {
	r.Log.Info("Reconciling alertRelabelConfigSecret")

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.alertRelabelConfigSecret, func() error {
		if err := r.own(r.alertRelabelConfigSecret); err != nil {
			return err
		}

		alertRelabelConfig := []struct {
			TargetLabel string `yaml:"target_label,omitempty"`
			Replacement string `yaml:"replacement,omitempty"`
		}{{
			TargetLabel: "namespace",
			Replacement: r.namespace,
		}}

		config, err := yaml.Marshal(alertRelabelConfig)
		if err != nil {
			return fmt.Errorf("Unable to encode alert relabel conifg: %v", err)
		}
		r.alertRelabelConfigSecret.Data = map[string][]byte{
			alertRelabelConfigSecretKey: config,
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("Unable to create/update AlertRelabelConfigSecret: %v", err)
	}

	return nil
}

func (r *ManagedMCGReconciler) reconcileStorageCluster() error {
	r.Log.Info("Reconciling StorageCluster")
	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.storageCluster, func() error {
		sc := templates.StorageClusterTemplate.DeepCopy()
		r.storageCluster.Spec = sc.Spec
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *ManagedMCGReconciler) reconcileConsoleCluster() error {

	consoleList := operatorv1.ConsoleList{}
	if err := r.Client.List(r.ctx, &consoleList); err == nil {
		for _, cluster := range consoleList.Items {
			if utils.Contains(cluster.Spec.Plugins, odfConsoleName) {
				r.Log.Info("Cluster instnce already exists.")
				return nil
			}
		}
	}

	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.operatorConsole, func() error {
		r.operatorConsole.Spec.Plugins = append(r.operatorConsole.Spec.Plugins, odfConsoleName)
		return nil
	})
	return err
}

func (r *ManagedMCGReconciler) reconcileODFCSV() error {
	var csv *opv1a1.ClusterServiceVersion
	var err error
	if csv, err = r.getCSVByPrefix(odfOperatorName); err != nil {
		return err
	}
	var isChanged bool
	deployments := csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs
	for i := range deployments {
		containers := deployments[i].Spec.Template.Spec.Containers
		for j := range containers {
			container := &containers[j]
			name := container.Name
			switch name {
			case "manager":
				resources := utils.GetResourceRequirements("odf-operator")
				if !equality.Semantic.DeepEqual(container.Resources, resources) {
					container.Resources = resources
					isChanged = true
				}
			default:
				r.Log.Info("Could not find resource requirement", "Resource", container.Name)
			}
		}
	}
	if isChanged {
		if err := r.update(csv); err != nil {
			return fmt.Errorf("Failed to update ODF CSV with resource requirements: %v", err)
		}
	}
	return nil
}

func (r *ManagedMCGReconciler) reconcileNoobaa() error {
	r.Log.Info("Reconciling Noobaa")
	noobaList := noobaa.NooBaaList{}
	if err := r.list(&noobaList); err == nil {
		for _, noobaa := range noobaList.Items {
			if noobaa.Name == noobaaName {
				r.Log.Info("Noobaa instnce already exists.")
				return nil
			}
		}
	}
	desiredNoobaa := templates.NoobaTemplate.DeepCopy()
	err := r.setNooBaaDesiredState(desiredNoobaa)
	_, err = ctrl.CreateOrUpdate(r.ctx, r.Client, r.noobaa, func() error {
		if err := r.own(r.storageSystem); err != nil {
			return err
		}
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
func (r *ManagedMCGReconciler) reconcileStorageSystem() error {
	r.Log.Info("Reconciling StorageSystem.")

	ssList := odfv1alpha1.StorageSystemList{}
	if err := r.list(&ssList); err == nil {
		for _, storageSyste := range ssList.Items {
			if storageSyste.Name == storageSystemName {
				r.Log.Info("storageSystem instnce already exists.")
				return nil
			}
		}
	}
	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.storageSystem, func() error {

		if r.reconcileStrategy == mcgv1alpha1.ReconcileStrategyStrict {
			var desired *odfv1alpha1.StorageSystem = nil
			var err error
			if desired, err = r.getDesiredConvergedStorageSystem(); err != nil {
				return err
			}
			r.storageSystem.Spec = desired.Spec
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *ManagedMCGReconciler) getDesiredConvergedStorageSystem() (*odfv1alpha1.StorageSystem, error) {
	ss := templates.StorageSystemTemplate.DeepCopy()
	ss.Spec.Name = storageClusterName
	ss.Spec.Namespace = r.namespace
	return ss, nil
}

func (r *ManagedMCGReconciler) verifyComponentsDoNotExist() bool {
	subComponent := r.managedMCG.Status.Components

	if subComponent.Noobaa.State == mcgv1alpha1.ComponentNotFound {
		return true
	}
	return false
}

func (r *ManagedMCGReconciler) reconcileODFOperatorMgrConfig() error {
	r.Log.Info("Reconciling odf-operator-manager-config ConfigMap")
	_, err := ctrl.CreateOrUpdate(r.ctx, r.Client, r.odfOperatorManagerconfigMap, func() error {
		r.odfOperatorManagerconfigMap.Data["ODF_SUBSCRIPTION_NAME"] = "odf-operator-stable-4.9-redhat-operators-openshift-marketplace"
		r.odfOperatorManagerconfigMap.Data["NOOBAA_SUBSCRIPTION_STARTINGCSV"] = "mcg-operator.v4.9.2"

		return nil
	})
	if err != nil {
		return err
	}
	return nil
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
				} else if name == r.AddonConfigMapName {
					if _, ok := client.GetLabels()[r.AddonConfigMapDeleteLabelKey]; ok {
						return true
					}
				}
				return false
			},
		),
	)

	odfPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return strings.HasPrefix(client.GetName(), odfOperatorName)
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

	ssPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return strings.HasPrefix(client.GetName(), storageSystemName)
			},
		),
	)

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlOptions).
		For(&mcgv1alpha1.ManagedMCG{}, managedMCGredicates).

		// Watch owned resources
		Owns(&ocsv1.StorageCluster{}).
		Owns(&noobaa.NooBaa{}).
		Owns(&odfv1alpha1.StorageSystem{}).

		// Watch non-owned resources
		Watches(
			&source.Kind{Type: &corev1.ConfigMap{}},
			enqueueManangedMCGRequest,
			configMapPredicates,
		).
		Watches(
			&source.Kind{Type: &odfv1alpha1.StorageSystem{}},
			enqueueManangedMCGRequest,
			ssPredicates,
		).
		Watches(
			&source.Kind{Type: &noobaa.NooBaa{}},
			enqueueManangedMCGRequest,
			noobaaPredicates,
		).
		Watches(
			&source.Kind{Type: &opv1a1.ClusterServiceVersion{}},
			enqueueManangedMCGRequest,
			odfPredicates,
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
