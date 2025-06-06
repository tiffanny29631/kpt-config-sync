// Copyright 2019 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cli-utils/pkg/apis/actuation"
	"sigs.k8s.io/cli-utils/pkg/apply/cache"
	"sigs.k8s.io/cli-utils/pkg/apply/event"
	"sigs.k8s.io/cli-utils/pkg/apply/filter"
	"sigs.k8s.io/cli-utils/pkg/apply/info"
	"sigs.k8s.io/cli-utils/pkg/apply/mutator"
	"sigs.k8s.io/cli-utils/pkg/apply/prune"
	"sigs.k8s.io/cli-utils/pkg/apply/solver"
	"sigs.k8s.io/cli-utils/pkg/apply/taskrunner"
	"sigs.k8s.io/cli-utils/pkg/common"
	"sigs.k8s.io/cli-utils/pkg/inventory"
	"sigs.k8s.io/cli-utils/pkg/kstatus/watcher"
	"sigs.k8s.io/cli-utils/pkg/object"
	"sigs.k8s.io/cli-utils/pkg/object/validation"
)

// Applier performs the step of applying a set of resources into a cluster,
// conditionally waits for all of them to be fully reconciled and finally
// performs prune to clean up any resources that has been deleted.
// The applier performs its function by executing a list queue of tasks,
// each of which is one of the steps in the process of applying a set
// of resources to the cluster. The actual execution of these tasks are
// handled by a StatusRunner. So the taskqueue is effectively a
// specification that is executed by the StatusRunner. Based on input
// parameters and/or the set of resources that needs to be applied to the
// cluster, different sets of tasks might be needed.
type Applier struct {
	pruner        *prune.Pruner
	statusWatcher watcher.StatusWatcher
	invClient     inventory.Client
	client        dynamic.Interface
	openAPIGetter discovery.OpenAPISchemaInterface
	mapper        meta.RESTMapper
	infoHelper    info.Helper
}

// prepareObjects returns the set of objects to apply and to prune or
// an error if one occurred.
func (a *Applier) prepareObjects(ctx context.Context, inv inventory.Inventory, localObjs object.UnstructuredSet,
	o ApplierOptions) (object.UnstructuredSet, object.UnstructuredSet, error) {
	if inv == nil {
		return nil, nil, fmt.Errorf("the local inventory can't be nil")
	}
	if err := inventory.ValidateNoInventory(localObjs); err != nil {
		return nil, nil, err
	}
	// Add the inventory annotation to the resources being applied.
	for _, localObj := range localObjs {
		inventory.AddInventoryIDAnnotation(localObj, inv.Info().GetID())
	}
	pruneObjs, err := a.pruner.GetPruneObjs(ctx, inv, localObjs, prune.Options{
		DryRunStrategy: o.DryRunStrategy,
	})
	if err != nil {
		return nil, nil, err
	}
	return localObjs, pruneObjs, nil
}

// Run performs the Apply step. This happens asynchronously with updates
// on progress and any errors reported back on the event channel.
// Cancelling the operation or setting timeout on how long to Wait
// for it complete can be done with the passed in context.
// Note: There isn't currently any way to interrupt the operation
// before all the given resources have been applied to the cluster. Any
// cancellation or timeout will only affect how long we Wait for the
// resources to become current.
func (a *Applier) Run(ctx context.Context, invInfo inventory.Info, objects object.UnstructuredSet, options ApplierOptions) <-chan event.Event {
	klog.V(4).Infof("apply run for %d objects", len(objects))
	eventChannel := make(chan event.Event)
	setDefaults(&options)
	go func() {
		defer close(eventChannel)
		// Validate the resources to make sure we catch those problems early
		// before anything has been updated in the cluster.
		vCollector := &validation.Collector{}
		validator := &validation.Validator{
			Collector: vCollector,
			Mapper:    a.mapper,
		}
		validator.Validate(objects)

		inv, err := a.invClient.Get(ctx, invInfo, inventory.GetOptions{})
		if apierrors.IsNotFound(err) {
			inv, err = a.invClient.NewInventory(invInfo)
		}
		if err != nil {
			handleError(eventChannel, err)
			return
		}
		if inv.Info().GetID() != invInfo.GetID() {
			handleError(eventChannel, fmt.Errorf("expected inventory object to have inventory-id %q but got %q",
				invInfo.GetID(), inv.Info().GetID()))
			return
		}

		// Decide which objects to apply and which to prune
		applyObjs, pruneObjs, err := a.prepareObjects(ctx, inv, objects, options)
		if err != nil {
			handleError(eventChannel, err)
			return
		}
		klog.V(4).Infof("calculated %d apply objs; %d prune objs", len(applyObjs), len(pruneObjs))

		// Build a TaskContext for passing info between tasks
		resourceCache := cache.NewResourceCacheMap()
		taskContext := taskrunner.NewTaskContext(ctx, eventChannel, resourceCache)

		// Fetch the queue (channel) of tasks that should be executed.
		klog.V(4).Infoln("applier building task queue...")
		// Build list of apply validation filters.
		applyFilters := []filter.ValidationFilter{
			filter.InventoryPolicyApplyFilter{
				Client:    a.client,
				Mapper:    a.mapper,
				Inv:       invInfo,
				InvPolicy: options.InventoryPolicy,
			},
			filter.DependencyFilter{
				TaskContext:       taskContext,
				ActuationStrategy: actuation.ActuationStrategyApply,
				DryRunStrategy:    options.DryRunStrategy,
			},
		}
		// Build list of prune validation filters.
		pruneFilters := []filter.ValidationFilter{
			filter.PreventRemoveFilter{},
			filter.InventoryPolicyPruneFilter{
				Inv:       invInfo,
				InvPolicy: options.InventoryPolicy,
			},
			filter.LocalNamespacesFilter{
				LocalNamespaces: localNamespaces(invInfo, object.UnstructuredSetToObjMetadataSet(objects)),
			},
			filter.DependencyFilter{
				TaskContext:       taskContext,
				ActuationStrategy: actuation.ActuationStrategyDelete,
				DryRunStrategy:    options.DryRunStrategy,
			},
		}
		// Build list of apply mutators.
		applyMutators := []mutator.Interface{
			&mutator.ApplyTimeMutator{
				Client:        a.client,
				Mapper:        a.mapper,
				ResourceCache: resourceCache,
			},
		}
		taskBuilder := &solver.TaskQueueBuilder{
			Pruner:        a.pruner,
			DynamicClient: a.client,
			OpenAPIGetter: a.openAPIGetter,
			InfoHelper:    a.infoHelper,
			Mapper:        a.mapper,
			Inventory:     inv,
			InvClient:     a.invClient,
			Collector:     vCollector,
			ApplyFilters:  applyFilters,
			ApplyMutators: applyMutators,
			PruneFilters:  pruneFilters,
		}
		opts := solver.Options{
			ServerSideOptions:      options.ServerSideOptions,
			ReconcileTimeout:       options.ReconcileTimeout,
			Destroy:                false,
			Prune:                  !options.NoPrune,
			DryRunStrategy:         options.DryRunStrategy,
			PrunePropagationPolicy: options.PrunePropagationPolicy,
			PruneTimeout:           options.PruneTimeout,
			InventoryPolicy:        options.InventoryPolicy,
		}

		// Build the ordered set of tasks to execute.
		taskQueue := taskBuilder.
			WithApplyObjects(applyObjs).
			WithPruneObjects(pruneObjs).
			Build(taskContext, opts)

		klog.V(4).Infof("validation errors: %d", len(vCollector.Errors))
		klog.V(4).Infof("invalid objects: %d", len(vCollector.InvalidIDs))

		// Handle validation errors
		switch options.ValidationPolicy {
		case validation.ExitEarly:
			err = vCollector.ToError()
			if err != nil {
				handleError(eventChannel, err)
				return
			}
		case validation.SkipInvalid:
			for _, err := range vCollector.Errors {
				handleValidationError(eventChannel, err)
			}
		default:
			handleError(eventChannel, fmt.Errorf("invalid ValidationPolicy: %q", options.ValidationPolicy))
			return
		}

		// Register invalid objects to be retained in the inventory, if present.
		for _, id := range vCollector.InvalidIDs {
			taskContext.AddInvalidObject(id)
		}

		// Send event to inform the caller about the resources that
		// will be applied/pruned.
		eventChannel <- event.Event{
			Type: event.InitType,
			InitEvent: event.InitEvent{
				ActionGroups: taskQueue.ToActionGroups(),
			},
		}
		// Create a new TaskStatusRunner to execute the taskQueue.
		klog.V(4).Infoln("applier building TaskStatusRunner...")
		allIDs := object.UnstructuredSetToObjMetadataSet(append(applyObjs, pruneObjs...))
		statusWatcher := a.statusWatcher
		// Disable watcher for dry runs
		if opts.DryRunStrategy.ClientOrServerDryRun() {
			statusWatcher = watcher.BlindStatusWatcher{}
		}
		runner := taskrunner.NewTaskStatusRunner(allIDs, statusWatcher)
		klog.V(4).Infoln("applier running TaskStatusRunner...")
		err = runner.Run(ctx, taskContext, taskQueue.ToChannel(), taskrunner.Options{
			EmitStatusEvents:         options.EmitStatusEvents,
			WatcherRESTScopeStrategy: options.WatcherRESTScopeStrategy,
		})
		if err != nil {
			handleError(eventChannel, err)
			return
		}
	}()
	return eventChannel
}

type ApplierOptions struct {
	// Encapsulates the fields for server-side apply.
	ServerSideOptions common.ServerSideOptions

	// ReconcileTimeout defines whether the applier should wait
	// until all applied resources have been reconciled, and if so,
	// how long to wait.
	ReconcileTimeout time.Duration

	// EmitStatusEvents defines whether status events should be
	// emitted on the eventChannel to the caller.
	EmitStatusEvents bool

	// NoPrune defines whether pruning of previously applied
	// objects should happen after apply.
	NoPrune bool

	// DryRunStrategy defines whether changes should actually be performed,
	// or if it is just talk and no action.
	DryRunStrategy common.DryRunStrategy

	// PrunePropagationPolicy defines the deletion propagation policy
	// that should be used for pruning. If this is not provided, the
	// default is to use the Background policy.
	PrunePropagationPolicy metav1.DeletionPropagation

	// PruneTimeout defines whether we should wait for all resources
	// to be fully deleted after pruning, and if so, how long we should
	// wait.
	PruneTimeout time.Duration

	// InventoryPolicy defines the inventory policy of apply.
	InventoryPolicy inventory.Policy

	// ValidationPolicy defines how to handle invalid objects.
	ValidationPolicy validation.Policy

	// RESTScopeStrategy specifies which strategy to use when listing and
	// watching resources. By default, the strategy is selected automatically.
	WatcherRESTScopeStrategy watcher.RESTScopeStrategy
}

// setDefaults set the options to the default values if they
// have not been provided.
func setDefaults(o *ApplierOptions) {
	if o.PrunePropagationPolicy == "" {
		o.PrunePropagationPolicy = metav1.DeletePropagationBackground
	}
}

func handleError(eventChannel chan event.Event, err error) {
	eventChannel <- event.Event{
		Type: event.ErrorType,
		ErrorEvent: event.ErrorEvent{
			Err: err,
		},
	}
}

// localNamespaces stores a set of strings of all the namespaces
// for the passed non cluster-scoped localObjs, plus the namespace
// of the passed inventory object. This is used to skip deleting
// namespaces which have currently applied objects in them.
func localNamespaces(localInv inventory.Info, localObjs []object.ObjMetadata) sets.String { // nolint:staticcheck
	namespaces := sets.NewString()
	for _, obj := range localObjs {
		if obj.Namespace != "" {
			namespaces.Insert(obj.Namespace)
		}
	}
	invNamespace := localInv.GetNamespace()
	if invNamespace != "" {
		namespaces.Insert(invNamespace)
	}
	return namespaces
}

func handleValidationError(eventChannel chan<- event.Event, err error) {
	switch tErr := err.(type) {
	case *validation.Error:
		// handle validation error about one or more specific objects
		eventChannel <- event.Event{
			Type: event.ValidationType,
			ValidationEvent: event.ValidationEvent{
				Identifiers: tErr.Identifiers(),
				Error:       tErr,
			},
		}
	default:
		// handle general validation error (no specific object)
		eventChannel <- event.Event{
			Type: event.ValidationType,
			ValidationEvent: event.ValidationEvent{
				Error: tErr,
			},
		}
	}
}
