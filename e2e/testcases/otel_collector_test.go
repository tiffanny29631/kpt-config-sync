// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	monitoringv2 "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/golang/protobuf/ptypes/timestamp"
	"go.uber.org/multierr"
	"google.golang.org/api/iterator"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"kpt.dev/configsync/e2e"
	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/iam"
	"kpt.dev/configsync/e2e/nomostest/ntopts"
	"kpt.dev/configsync/e2e/nomostest/retry"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/e2e/nomostest/workloadidentity"
	"kpt.dev/configsync/pkg/api/configmanagement"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/core"
	"kpt.dev/configsync/pkg/core/k8sobjects"
	"kpt.dev/configsync/pkg/kinds"
	"kpt.dev/configsync/pkg/metrics"
	csmetrics "kpt.dev/configsync/pkg/metrics"
	rgmetrics "kpt.dev/configsync/pkg/resourcegroup/controllers/metrics"
)

const (
	DefaultMonitorKSA             = "default"
	MonitorGSA                    = "e2e-test-metric-writer"
	MetricExportErrorCaption      = "One or more TimeSeries could not be written"
	UnrecognizedLabelErrorCaption = "unrecognized metric labels"
	GCMMetricPrefix               = "custom.googleapis.com/opencensus/config_sync"
)

// The default list of exported metrics according to otel.go filter/cloudmonitoring
// Skipping resource_fights_total and internal_errors_total during validation
// as they don't appear in query results when no error condition exists. A later
// work is in progress to initialize this type of metric.
var DefaultGCMMetricTypes = []string{
	csmetrics.APICallDurationName,
	csmetrics.ReconcilerErrorsName,
	csmetrics.PipelineErrorName, // name reused in resource group controller
	csmetrics.ReconcileDurationName,
	csmetrics.LastSyncName,
	csmetrics.DeclaredResourcesName,
	csmetrics.ApplyOperationsName,
	csmetrics.ApplyDurationName,
	rgmetrics.RGReconcileDurationName,
	rgmetrics.ResourceCountName,
	rgmetrics.ReadyResourceCountName,
	rgmetrics.KCCResourceCountName,
	rgmetrics.ClusterScopedResourceCountName,
	rgmetrics.NamespaceCountName,
}

var GCMMetricTypes = []string{
	csmetrics.APICallDurationName,
	csmetrics.ReconcilerErrorsName,
	csmetrics.PipelineErrorName, // name reused in resource group controller
	csmetrics.ReconcileDurationName,
	csmetrics.ParserDurationName,
	csmetrics.LastSyncName,
	csmetrics.DeclaredResourcesName,
	csmetrics.ApplyOperationsName,
	csmetrics.ApplyDurationName,
	//csmetrics.ResourceFightsName,
	csmetrics.RemediateDurationName,
	csmetrics.LastApplyName,
	//csmetrics.ResourceConflictsName,
	//csmetrics.InternalErrorsName,
	rgmetrics.RGReconcileDurationName,
	rgmetrics.ResourceGroupTotalName,
	rgmetrics.ResourceCountName,
	rgmetrics.ReadyResourceCountName,
	rgmetrics.KCCResourceCountName,
	rgmetrics.NamespaceCountName,
	rgmetrics.ClusterScopedResourceCountName,
	rgmetrics.CRDCountName,
}

// ReconcilerMetricTypes is a minimal set of reconciler metrics used for sync label validation in tests.
// This subset reduces the number of GCM API calls, as sync-related labels are injected via environment variables
// and will appear on all reconciler metrics if present on any. Thus, validating this subset is sufficient.
var ReconcilerMetricTypes = []string{
	csmetrics.DeclaredResourcesName,
	csmetrics.ApplyOperationsName,
}

var syncLabels = map[string]string{
	"configsync_sync_name":      configsync.RootSyncName,
	"configsync_sync_kind":      configsync.RootSyncKind,
	"configsync_sync_namespace": configmanagement.ControllerNamespace,
}

// TestOtelCollectorDeployment validates that metrics reporting works for
// Google Cloud Monitoring using either workload identity or node identity.
//
// Requirements:
// - node identity:
//   - node GSA with roles/monitoring.metricWriter IAM
//
// - workload identity:
//   - e2e-test-metric-writer GSA with roles/monitoring.metricWriter IAM
//   - roles/iam.workloadIdentityUser on config-management-monitoring/default for e2e-test-metric-writer
func TestOtelCollectorDeployment(t *testing.T) {
	rootSyncID := nomostest.DefaultRootSyncID
	nt := nomostest.New(t,
		nomostesting.Reconciliation1,
		ntopts.RequireGKE(t),
		ntopts.SyncWithGitSource(rootSyncID, ntopts.Unstructured),
	)
	rootSyncGitRepo := nt.SyncSourceGitReadWriteRepository(rootSyncID)
	nt.T.Cleanup(func() {
		if t.Failed() {
			nt.PodLogs("config-management-monitoring", csmetrics.OtelCollectorName, "", false)
		}
	})
	setupMetricsServiceAccount(nt)
	nt.T.Cleanup(func() {
		nt.MustKubectl("delete", "-f", "../testdata/otel-collector/otel-cm-monarch-rejected-labels.yaml", "--ignore-not-found")
		nt.MustKubectl("delete", "-f", "../testdata/otel-collector/otel-cm-kustomize-rejected-labels.yaml", "--ignore-not-found")
	})

	startTime := time.Now().UTC()

	nt.T.Log("Adding test commit after otel-collector is started up so multiple commit hashes are processed in pipelines")
	namespace := k8sobjects.NamespaceObject("foo")
	nt.Must(rootSyncGitRepo.Add("acme/ns.yaml", namespace))
	nt.Must(rootSyncGitRepo.CommitAndPush("Adding foo namespace"))
	nt.Must(nt.WatchForAllSyncs())

	nt.T.Log("Watch for metrics in GCM, timeout 2 minutes")
	ctx := nt.Context
	client, err := createGCMClient(ctx)
	if err != nil {
		nt.T.Fatal(err)
	}
	// retry for 2 minutes until metric is accessible from GCM
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, DefaultGCMMetricTypes))

	nt.T.Log("Checking the otel-collector log contains no failure...")
	err = validateDeploymentLogHasNoFailure(nt, csmetrics.OtelCollectorName, configmanagement.MonitoringNamespace, MetricExportErrorCaption, startTime)
	if err != nil {
		nt.T.Fatal(err)
	}

	// The ConfigMap that is expected to trigger duplicate time series error has
	// name 'otel-collector-custom', which by the setup in otel-collector deployment
	// will take precedence over 'otel-collector-googlecloud' that was deployed
	// by the test.
	nt.T.Log("Apply custom otel-collector ConfigMap that could cause duplicate time series error")
	nt.MustKubectl("apply", "-f", "../testdata/otel-collector/otel-cm-monarch-rejected-labels.yaml")

	nt.T.Log("Checking the otel-collector log contains failure...")
	_, err = retry.Retry(60*time.Second, func() error {
		return validateDeploymentLogHasFailure(nt, csmetrics.OtelCollectorName, configmanagement.MonitoringNamespace, MetricExportErrorCaption, startTime)
	})
	if err != nil {
		nt.T.Fatal(err)
	}

	nt.T.Log("Remove otel-collector ConfigMap that creates duplicated time series error")
	nt.MustKubectl("delete", "-f", "../testdata/otel-collector/otel-cm-monarch-rejected-labels.yaml", "--ignore-not-found")

	// Change the RootSync to sync from kustomize-components dir to enable Kustomize metrics
	nt.T.Log("Add the kustomize components root directory to enable kustomize metrics")
	nt.Must(rootSyncGitRepo.Copy("../testdata/hydration/kustomize-components", "."))
	nt.Must(rootSyncGitRepo.CommitAndPush("add DRY configs to the repository"))

	nt.T.Log("Update RootSync to sync from the kustomize-components directory")
	rs := k8sobjects.RootSyncObjectV1Beta1(configsync.RootSyncName)
	nt.MustMergePatch(rs, `{"spec": {"git": {"dir": "kustomize-components"}}}`)
	nomostest.SetExpectedSyncPath(nt, rootSyncID, "kustomize-components")
	nt.Must(nt.WatchForAllSyncs())

	// retry for 2 minutes until metric is accessible from GCM
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, DefaultGCMMetricTypes))

	nt.T.Log("Checking the otel-collector log contains no failure...")
	err = validateDeploymentLogHasNoFailure(nt, csmetrics.OtelCollectorName, configmanagement.MonitoringNamespace, MetricExportErrorCaption, startTime)
	if err != nil {
		nt.T.Fatal(err)
	}

	nt.T.Log("Apply custom otel-collector ConfigMap that could cause Monarch label rejected error")
	nt.MustKubectl("apply", "-f", "../testdata/otel-collector/otel-cm-kustomize-rejected-labels.yaml")
	if err := nt.Watcher.WatchForCurrentStatus(kinds.Deployment(), csmetrics.OtelCollectorName, configmanagement.MonitoringNamespace); err != nil {
		nt.T.Fatal(err)
	}

	nt.T.Log("Checking the otel-collector log contains failure...")
	_, err = retry.Retry(60*time.Second, func() error {
		return validateDeploymentLogHasFailure(nt, csmetrics.OtelCollectorName, configmanagement.MonitoringNamespace, UnrecognizedLabelErrorCaption, startTime)
	})
	if err != nil {
		nt.T.Fatal(err)
	}
}

func TestGCMMetrics(t *testing.T) {
	nt := nomostest.New(t,
		nomostesting.Reconciliation1,
		ntopts.RequireGKE(t),
		ntopts.SyncWithGitSource(nomostest.DefaultRootSyncID, ntopts.Unstructured),
	)
	rootSyncGitRepo := nt.SyncSourceGitReadWriteRepository(nomostest.DefaultRootSyncID)
	nt.T.Cleanup(func() {
		if t.Failed() {
			nt.PodLogs("config-management-monitoring", csmetrics.OtelCollectorName, "", false)
		}
	})
	setupMetricsServiceAccount(nt)
	nt.T.Cleanup(func() {
		nt.MustKubectl("delete", "-f", "../testdata/otel-collector/otel-cm-full-gcm.yaml", "--ignore-not-found")
	})

	nt.T.Log("Apply custom otel-collector ConfigMap that exports full metric list to GCM")
	nt.MustKubectl("apply", "-f", "../testdata/otel-collector/otel-cm-full-gcm.yaml")

	startTime := time.Now().UTC()

	nt.T.Log("Watch for full list of metrics in GCM, timeout 2 minutes")
	ctx := nt.Context
	client, err := createGCMClient(ctx)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.T.Log("validate all metrics are present in GCM")
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, GCMMetricTypes))

	nt.T.Log("validate sync labels are present on reconciler metrics")
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, ReconcilerMetricTypes, metricHasLabels(syncLabels)))

	nt.T.Log("Adding test namespace")
	namespace := k8sobjects.NamespaceObject("foo")
	nt.Must(rootSyncGitRepo.Add("acme/ns.yaml", namespace))
	nt.Must(rootSyncGitRepo.CommitAndPush("Adding foo namespace"))
	nt.Must(nt.WatchForAllSyncs())

	nt.T.Log("Checking resource related metrics after adding test resource")
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, []string{csmetrics.DeclaredResourcesName}, metricHasValue(3)))

	nt.T.Log("Remove the test resource")
	nt.Must(rootSyncGitRepo.Remove("acme/ns.yaml"))
	nt.Must(rootSyncGitRepo.CommitAndPush("Remove the test namespace"))
	nt.Must(nt.WatchForAllSyncs())

	nt.T.Log("Checking resource related metrics after removing test resource")
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, []string{csmetrics.DeclaredResourcesName}, metricHasLatestValue(2)))
}

// TestOtelCollectorGCMLabelAggregation validates that Google Cloud Monitoring
// metrics to ensure that the "commit" label is removed through aggregation in
// the otel-collector config.
//
// Requirements:
// - node identity:
//   - node GSA with roles/monitoring.metricWriter IAM
//
// - workload identity:
//   - e2e-test-metric-writer GSA with roles/monitoring.metricWriter IAM
//   - roles/iam.workloadIdentityUser on config-management-monitoring/default for e2e-test-metric-writer
func TestOtelCollectorGCMLabelAggregation(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1, ntopts.RequireGKE(t))
	rootSyncGitRepo := nt.SyncSourceGitReadWriteRepository(nomostest.DefaultRootSyncID)
	setupMetricsServiceAccount(nt)

	startTime := time.Now().UTC()

	nt.T.Log("Adding test commit")
	namespace := k8sobjects.NamespaceObject("foo")
	nt.Must(rootSyncGitRepo.Add("acme/ns.yaml", namespace))
	nt.Must(rootSyncGitRepo.CommitAndPush("Adding foo namespace"))
	nt.Must(nt.WatchForAllSyncs())

	// The following metrics are sent to GCM and aggregated to remove the "commit" label.
	var metricsWithCommitLabel = []string{
		csmetrics.LastSyncName,
		csmetrics.DeclaredResourcesName,
		csmetrics.ApplyDurationName,
	}

	nt.T.Log("Watch for metrics in GCM, timeout 2 minutes")
	ctx := nt.Context
	client, err := createGCMClient(nt.Context)
	if err != nil {
		nt.T.Fatal(err)
	}
	// retry for 2 minutes until metric is accessible from GCM
	nt.Must(validateMetricTypes(ctx, nt, client, startTime, metricsWithCommitLabel, metricDoesNotHaveLabel(metrics.KeyCommit.Name())))
}

func setupMetricsServiceAccount(nt *nomostest.NT) {
	workloadPool, err := workloadidentity.GetWorkloadPool(nt)
	if err != nil {
		nt.T.Fatal(err)
	}
	// If Workload Identity enabled on cluster, setup KSA to GSA annotation.
	// Otherwise, the node identity is used.
	if workloadPool != "" {
		gsaEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", MonitorGSA, *e2e.GCPProject)
		if err := iam.ValidateServiceAccountExists(nt, gsaEmail); err != nil {
			nt.T.Fatal(err)
		}

		nt.T.Cleanup(func() {
			ksa := &corev1.ServiceAccount{}
			if err := nt.KubeClient.Get(DefaultMonitorKSA, configmanagement.MonitoringNamespace, ksa); err != nil {
				if apierrors.IsNotFound(err) {
					return // no need to remove annotation
				}
				nt.T.Fatalf("failed to get service account during cleanup: %v", err)
			}
			core.RemoveAnnotations(ksa, "iam.gke.io/gcp-service-account")
			if err := nt.KubeClient.Update(ksa); err != nil {
				nt.T.Fatalf("failed to remove service account annotation during cleanup: %v", err)
			}
		})

		nt.T.Log(fmt.Sprintf("Workload identity enabled, adding KSA annotation to use %s service account", MonitorGSA))
		ksa := &corev1.ServiceAccount{}
		if err := nt.KubeClient.Get(DefaultMonitorKSA, configmanagement.MonitoringNamespace, ksa); err != nil {
			nt.T.Fatalf("failed to get service account: %v", err)
		}
		if core.SetAnnotation(ksa, "iam.gke.io/gcp-service-account", gsaEmail) {
			if err := nt.KubeClient.Update(ksa); err != nil {
				nt.T.Fatalf("failed to set service account annotation: %v", err)
			}
		}
		nt.Must(nt.WatchForAllSyncs())
	}
}

func validateDeploymentLogHasFailure(nt *nomostest.NT, deployment, namespace, errorString string, startTime time.Time) error {
	entry, err := nt.GetPodLogs(namespace, deployment, "", false, &startTime)
	if err != nil {
		return err
	}
	for _, m := range entry {
		if strings.Contains(m, errorString) {
			return nil
		}
	}
	return fmt.Errorf("error expected in the log of deployment %s, namespace %s but found none", deployment, namespace)
}

func validateDeploymentLogHasNoFailure(nt *nomostest.NT, deployment, namespace, errorString string, startTime time.Time) error {
	entry, err := nt.GetPodLogs(namespace, deployment, "", false, &startTime)
	if err != nil {
		return err
	}
	for _, m := range entry {
		if strings.Contains(m, errorString) {
			return fmt.Errorf("failure found in the log of deployment %s, namespace %s: %s", deployment, namespace, m)
		}
	}
	return nil
}

// Create a new Monitoring service client using application default credentials
func createGCMClient(ctx context.Context) (*monitoringv2.MetricClient, error) {
	client, err := monitoringv2.NewMetricClient(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Make a ListTimeSeries request of a specific metric to GCM with specified
// metricType.
// Note: metricType in this context is the metric descriptor name, for example
// "custom.googleapis.com/opencensus/config_sync/apply_operations_total".
func listMetricInGCM(ctx context.Context, nt *nomostest.NT, client *monitoringv2.MetricClient, startTime time.Time, metricType string) *monitoringv2.TimeSeriesIterator {
	endTime := time.Now().UTC()
	req := &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + *e2e.GCPProject,
		Filter: `metric.type="` + metricType + `" AND resource.labels.cluster_name="` + nt.ClusterName + `" AND resource.type="k8s_container"`,
		Interval: &monitoringpb.TimeInterval{
			StartTime: &timestamp.Timestamp{
				Seconds: startTime.Unix(),
			},
			EndTime: &timestamp.Timestamp{
				Seconds: endTime.Unix(),
			},
		},
		View: monitoringpb.ListTimeSeriesRequest_FULL,
	}
	return client.ListTimeSeries(ctx, req)
}

type metricValidatorFunc func(series *monitoringpb.TimeSeries) error

func metricHasLabels(labelMap map[string]string) metricValidatorFunc {
	return func(series *monitoringpb.TimeSeries) error {
		metricLabels := series.GetMetric().GetLabels()
		for label, value := range labelMap {
			actual, found := metricLabels[label]
			if !found {
				return fmt.Errorf("expected metric to have label %s=%s, but found none", label, value)
			} else if actual != value {
				return fmt.Errorf("expected metric to have label %s=%s, but found %s=%s", label, value, label, actual)
			}
		}
		return nil
	}
}

func metricDoesNotHaveLabel(label string) metricValidatorFunc {
	return func(series *monitoringpb.TimeSeries) error {
		labels := series.GetResource().GetLabels()
		if value, found := labels[label]; found {
			return fmt.Errorf("expected metric to not have label, but found %s=%s", label, value)
		}
		return nil
	}
}

func metricHasValue(expectedValue int64) metricValidatorFunc {
	return func(series *monitoringpb.TimeSeries) error {
		points := series.GetPoints()
		var values []int64
		for _, point := range points {
			value := point.GetValue().GetInt64Value()
			if value == expectedValue {
				return nil
			}
			values = append(values, value)
		}
		return fmt.Errorf("expected metric to contain value %v but got %v", expectedValue, values)
	}
}

func metricHasLatestValue(expectedValue int64) metricValidatorFunc {
	return func(series *monitoringpb.TimeSeries) error {
		points := series.GetPoints()
		if len(points) == 0 {
			return fmt.Errorf("expected metric to have at least one point, but got none")
		}
		lastPoint := points[len(points)-1]
		value := lastPoint.GetValue().GetInt64Value()
		if value == expectedValue {
			return nil
		}
		return fmt.Errorf("expected metric to have latest value %v but got %v", expectedValue, value)
	}
}

// validateMetricTypes checks all provided metric types in GCM for the given cluster, using the provided validator function for each metric.
func validateMetricTypes(ctx context.Context, nt *nomostest.NT, client *monitoringv2.MetricClient, startTime time.Time, metricTypes []string, valFns ...metricValidatorFunc) error {
	_, err := retry.Retry(120*time.Second, func() error {
		var err error
		for _, metricType := range metricTypes {
			descriptor := fmt.Sprintf("%s/%s", GCMMetricPrefix, metricType)
			it := listMetricInGCM(ctx, nt, client, startTime, descriptor)
			err = multierr.Append(err, validateMetricSeries(nt, it, descriptor, valFns...))
		}
		return err
	})
	return err
}

// Validates a metricType from a specific cluster_name can be found within given
// TimeSeries
func validateMetricSeries(nt *nomostest.NT, it *monitoringv2.TimeSeriesIterator, metricType string, valFns ...metricValidatorFunc) error {
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("received nil TimeSeries response for metric %s", metricType)
		}
		metric := resp.GetMetric()
		resource := resp.GetResource()
		nt.Logger.Debugf(`GCM metric result: { "type": %q, "labels": %+v, "resource.type": %q, "resource.labels": %+v }`,
			metric.Type, metric.Labels, resource.Type, resource.Labels)
		if metric.GetType() == metricType {
			labels := resource.GetLabels()
			if labels["cluster_name"] == nt.ClusterName {
				for _, valFn := range valFns {
					if err := valFn(resp); err != nil {
						return fmt.Errorf("GCM metric %s failed validation (cluster_name=%s): %w", metricType, nt.ClusterName, err)
					}
				}
				return nil
			}
		}
	}
	return fmt.Errorf("GCM metric %s not found (cluster_name=%s)",
		metricType, nt.ClusterName)
}
