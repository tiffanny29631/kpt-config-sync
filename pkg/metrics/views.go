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
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

// distributionBounds defines the bounds for a histogram distribution meansuring short durations.
var distributionBounds = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// longDistributionBounds defines the bounds for a histogram distribution meansuring long durations.
var longDistributionBounds = []float64{1, 5, 10, 30, 60, 300, 600, 1200, 1800, 3600, 5400}

var (
	// APICallDurationView aggregates the APICallDuration metric measurements.
	APICallDurationView = &view.View{
		Name:        APICallDurationName,
		Measure:     APICallDuration,
		Description: "The latency distribution of API server calls",
		TagKeys:     []tag.Key{KeyOperation, KeyStatus},
		Aggregation: view.Distribution(distributionBounds...),
	}

	// ReconcilerErrorsView aggregates the ReconcilerErrors metric measurements.
	ReconcilerErrorsView = &view.View{
		Name:        ReconcilerErrorsName,
		Measure:     ReconcilerErrors,
		Description: "The current number of errors in the RootSync and RepoSync reconcilers",
		TagKeys:     []tag.Key{KeyComponent, KeyErrorClass},
		Aggregation: view.LastValue(),
	}

	// PipelineErrorView aggregates the PipelineError metric measurements.
	// Definition here must exactly match the definition in the resource-group
	// controller, or the Prometheus exporter will error. b/247516388
	// https://github.com/GoogleContainerTools/kpt-resource-group/blob/main/controllers/metrics/views.go#L123
	PipelineErrorView = &view.View{
		Name:        PipelineErrorName,
		Measure:     PipelineError,
		Description: "A boolean value indicates if error happened from different stages when syncing a commit",
		TagKeys:     []tag.Key{KeyName, KeyReconcilerType, KeyComponent},
		Aggregation: view.LastValue(),
	}

	// ReconcileDurationView aggregates the ReconcileDuration metric measurements.
	ReconcileDurationView = &view.View{
		Name:        ReconcileDurationName,
		Measure:     ReconcileDuration,
		Description: "The latency distribution of RootSync and RepoSync reconcile events",
		TagKeys:     []tag.Key{KeyStatus},
		Aggregation: view.Distribution(distributionBounds...),
	}

	// ParserDurationView aggregates the ParserDuration metric measurements.
	ParserDurationView = &view.View{
		Name:        ParserDurationName,
		Measure:     ParserDuration,
		Description: "The latency distribution of the parse-apply-watch loop",
		TagKeys:     []tag.Key{KeyStatus, KeyTrigger, KeyParserSource},
		Aggregation: view.Distribution(longDistributionBounds...),
	}

	// LastSyncTimestampView aggregates the LastSyncTimestamp metric measurements.
	LastSyncTimestampView = &view.View{
		Name:        LastSyncName,
		Measure:     LastSync,
		Description: "The timestamp of the most recent sync from Git",
		TagKeys:     []tag.Key{KeyCommit, KeyStatus},
		Aggregation: view.LastValue(),
	}

	// DeclaredResourcesView aggregates the DeclaredResources metric measurements.
	DeclaredResourcesView = &view.View{
		Name:        DeclaredResourcesName,
		Measure:     DeclaredResources,
		Description: "The current number of declared resources parsed from Git",
		TagKeys:     []tag.Key{KeyCommit},
		Aggregation: view.LastValue(),
	}

	// ApplyOperationsView aggregates the ApplyOps metric measurements.
	ApplyOperationsView = &view.View{
		Name:        ApplyOperationsName,
		Measure:     ApplyOperations,
		Description: "The total number of operations that have been performed to sync resources to source of truth",
		TagKeys:     []tag.Key{KeyController, KeyOperation, KeyStatus},
		Aggregation: view.Count(),
	}

	// ApplyDurationView aggregates the ApplyDuration metric measurements.
	ApplyDurationView = &view.View{
		Name:        ApplyDurationName,
		Measure:     ApplyDuration,
		Description: "The latency distribution of applier resource sync events",
		TagKeys:     []tag.Key{KeyCommit, KeyStatus},
		Aggregation: view.Distribution(longDistributionBounds...),
	}

	// LastApplyTimestampView aggregates the LastApplyTimestamp metric measurements.
	LastApplyTimestampView = &view.View{
		Name:        LastApplyName,
		Measure:     LastApply,
		Description: "The timestamp of the most recent applier resource sync event",
		TagKeys:     []tag.Key{KeyCommit, KeyStatus},
		Aggregation: view.LastValue(),
	}

	// ResourceFightsView aggregates the ResourceFights metric measurements.
	ResourceFightsView = &view.View{
		Name:        ResourceFightsName,
		Measure:     ResourceFights,
		Description: "The total number of resources that are being synced too frequently",
		Aggregation: view.Count(),
	}

	// RemediateDurationView aggregates the RemediateDuration metric measurements.
	RemediateDurationView = &view.View{
		Name:        RemediateDurationName,
		Measure:     RemediateDuration,
		Description: "The latency distribution of remediator reconciliation events",
		TagKeys:     []tag.Key{KeyStatus},
		Aggregation: view.Distribution(distributionBounds...),
	}

	// ResourceConflictsView aggregates the ResourceConflicts metric measurements.
	ResourceConflictsView = &view.View{
		Name:        ResourceConflictsName,
		Measure:     ResourceConflicts,
		Description: "The total number of resource conflicts resulting from a mismatch between the cached resources and cluster resources",
		TagKeys:     []tag.Key{KeyCommit},
		Aggregation: view.Count(),
	}

	// InternalErrorsView aggregates the InternalErrors metric measurements.
	InternalErrorsView = &view.View{
		Name:        InternalErrorsName,
		Measure:     InternalErrors,
		Description: "The total number of internal errors triggered by Config Sync",
		TagKeys:     []tag.Key{KeyInternalErrorSource},
		Aggregation: view.Count(),
	}
)
