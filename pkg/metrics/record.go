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

package metrics

import (
	"context"
	"os"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"k8s.io/klog/v2"
	"kpt.dev/configsync/pkg/api/configsync/v1beta1"
	"kpt.dev/configsync/pkg/reconcilermanager"
	"kpt.dev/configsync/pkg/status"
)

// Initialization is the tag value to initiate a Count
const Initialization = "cs_counter_initialization"

func record(ctx context.Context, ms ...stats.Measurement) {
	stats.Record(ctx, ms...)
	if klog.V(5).Enabled() {
		for _, m := range ms {
			klog.Infof("Metric recorded: { \"Name\": %q, \"Value\": %#v, \"Tags\": %s }", m.Measure().Name(), m.Value(), tag.FromContext(ctx))
		}
	}
}

// RecordAPICallDuration produces a measurement for the APICallDuration view.
func RecordAPICallDuration(ctx context.Context, operation, status string, startTime time.Time) {
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyOperation, operation),
		tag.Upsert(KeyStatus, status))
	measurement := APICallDuration.M(time.Since(startTime).Seconds())
	record(tagCtx, measurement)
}

// RecordReconcilerErrors produces a measurement for the ReconcilerErrors view.
func RecordReconcilerErrors(ctx context.Context, component string, errs []v1beta1.ConfigSyncError) {
	errorCountByClass := status.CountErrorByClass(errs)
	var supportedErrorClasses = []string{"1xxx", "2xxx", "9xxx"}
	for _, errorclass := range supportedErrorClasses {
		var errorCount int64
		if v, ok := errorCountByClass[errorclass]; ok {
			errorCount = v
		}
		tagCtx, _ := tag.New(ctx,
			tag.Upsert(KeyComponent, component),
			tag.Upsert(KeyErrorClass, errorclass),
		)
		measurement := ReconcilerErrors.M(errorCount)
		record(tagCtx, measurement)
	}
}

// RecordPipelineError produces a measurement for the PipelineError view
func RecordPipelineError(ctx context.Context, reconcilerType, component string, errLen int) {
	reconcilerName := os.Getenv(reconcilermanager.ReconcilerNameKey)
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyName, reconcilerName),
		tag.Upsert(KeyReconcilerType, reconcilerType),
		tag.Upsert(KeyComponent, component))
	if errLen > 0 {
		record(tagCtx, PipelineError.M(1))
	} else {
		record(tagCtx, PipelineError.M(0))
	}
}

// RecordReconcileDuration produces a measurement for the ReconcileDuration view.
func RecordReconcileDuration(ctx context.Context, status string, startTime time.Time) {
	tagCtx, _ := tag.New(ctx, tag.Upsert(KeyStatus, status))
	measurement := ReconcileDuration.M(time.Since(startTime).Seconds())
	record(tagCtx, measurement)
}

// RecordParserDuration produces a measurement for the ParserDuration view.
func RecordParserDuration(ctx context.Context, trigger, source, status string, startTime time.Time) {
	tagCtx, _ := tag.New(ctx, tag.Upsert(KeyStatus, status), tag.Upsert(KeyTrigger, trigger), tag.Upsert(KeyParserSource, source))
	measurement := ParserDuration.M(time.Since(startTime).Seconds())
	record(tagCtx, measurement)
}

// RecordLastSync produces a measurement for the LastSync view.
func RecordLastSync(ctx context.Context, status, commit string, timestamp time.Time) {
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyStatus, status),
		tag.Upsert(KeyCommit, commit))
	measurement := LastSync.M(timestamp.Unix())
	record(tagCtx, measurement)
}

// RecordDeclaredResources produces a measurement for the DeclaredResources view.
func RecordDeclaredResources(ctx context.Context, commit string, numResources int) {
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyCommit, commit))
	measurement := DeclaredResources.M(int64(numResources))
	record(tagCtx, measurement)
}

// RecordApplyOperation produces a measurement for the ApplyOperations view.
func RecordApplyOperation(ctx context.Context, controller, operation, status string) {
	tagCtx, _ := tag.New(ctx,
		//tag.Upsert(KeyName, GetResourceLabels()),
		tag.Upsert(KeyOperation, operation),
		tag.Upsert(KeyController, controller),
		tag.Upsert(KeyStatus, status))
	measurement := ApplyOperations.M(1)
	record(tagCtx, measurement)
}

// RecordApplyDuration produces measurements for the ApplyDuration and LastApplyTimestamp views.
func RecordApplyDuration(ctx context.Context, status, commit string, startTime time.Time) {
	if commit == "" {
		// TODO: Remove default value when otel-collector supports empty tag values correctly.
		commit = CommitNone
	}
	now := time.Now()
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyStatus, status),
		tag.Upsert(KeyCommit, commit),
	)
	durationMeasurement := ApplyDuration.M(now.Sub(startTime).Seconds())
	lastApplyMeasurement := LastApply.M(now.Unix())
	record(tagCtx, durationMeasurement, lastApplyMeasurement)
}

// RecordResourceFight produces measurements for the ResourceFights view.
func RecordResourceFight(ctx context.Context, _ string) {
	//tagCtx, _ := tag.New(ctx,
	//tag.Upsert(KeyName, GetResourceLabels()),
	//tag.Upsert(KeyOperation, operation),
	//)
	measurement := ResourceFights.M(1)
	record(ctx, measurement)
}

// RecordRemediateDuration produces measurements for the RemediateDuration view.
func RecordRemediateDuration(ctx context.Context, status string, startTime time.Time) {
	tagCtx, _ := tag.New(ctx,
		tag.Upsert(KeyStatus, status),
	)
	measurement := RemediateDuration.M(time.Since(startTime).Seconds())
	record(tagCtx, measurement)
}

// RecordResourceConflict produces measurements for the ResourceConflicts view.
func RecordResourceConflict(ctx context.Context, commit string) {
	tagCtx, _ := tag.New(ctx,
		// tag.Upsert(KeyName, GetResourceLabels()),
		tag.Upsert(KeyCommit, commit),
	)
	measurement := ResourceConflicts.M(1)
	record(tagCtx, measurement)
}

// RecordInternalError produces measurements for the InternalErrors view.
func RecordInternalError(ctx context.Context, source string) {
	tagCtx, _ := tag.New(ctx, tag.Upsert(KeyInternalErrorSource, source))
	measurement := InternalErrors.M(1)
	record(tagCtx, measurement)
}
