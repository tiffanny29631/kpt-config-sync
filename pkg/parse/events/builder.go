// Copyright 2024 Google LLC
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

package events

import (
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
)

// PublishingGroupBuilder oversees construction of event publishers.
//
// For now, the publishers are driven by clock-based delay and backoff timers.
type PublishingGroupBuilder struct {
	// Clock is used for time tracking, namely to simplify testing by allowing
	// a fake clock, instead of a RealClock.
	Clock clock.Clock
	// SyncPeriod is the period of time between checking the filesystem
	// for publisher updates to sync.
	SyncPeriod time.Duration
	// StatusUpdatePeriod is how long the Parser waits between updates of the
	// sync status, to account for management conflict errors from the Remediator.
	StatusUpdatePeriod time.Duration
	// NamespaceControllerPeriod is how long to wait between checks to see if
	// the namespace-controller wants to trigger a resync.
	// TODO: Use a channel, instead of a timer checking a locked variable.
	NamespaceControllerPeriod time.Duration
	// RetryBackoff is how long the Parser waits between retries, after an error.
	RetryBackoff wait.Backoff
}

// Build a list of Publishers based on the PublishingGroupBuilder config.
func (t *PublishingGroupBuilder) Build() []Publisher {
	var publishers []Publisher
	if t.SyncPeriod > 0 {
		// ResetOnRunAttemptPublisher makes it so that the sync timer is reset whenever
		// a reconcile occurs due to one of the other event types.
		publishers = append(publishers, NewResetOnRunAttemptPublisher(SyncEventType, t.Clock, t.SyncPeriod))
	}
	if t.NamespaceControllerPeriod > 0 {
		publishers = append(publishers, NewTimeDelayPublisher(NamespaceSyncEventType, t.Clock, t.NamespaceControllerPeriod))
	}
	if t.RetryBackoff.Duration > 0 {
		publishers = append(publishers, NewRetrySyncPublisher(t.Clock, t.RetryBackoff))
	}
	if t.StatusUpdatePeriod > 0 {
		// Status updates are on an independent timer from the sync event, and thus
		// could run immediately after a sync event. The status updater performs a
		// diff before making API calls so this should be low cost.
		publishers = append(publishers, NewTimeDelayPublisher(StatusUpdateEventType, t.Clock, t.StatusUpdatePeriod))
	}
	return publishers
}
