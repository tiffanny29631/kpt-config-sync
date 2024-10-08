// Copyright 2023 Google LLC
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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/ntopts"
	"kpt.dev/configsync/e2e/nomostest/taskgroup"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/e2e/nomostest/testpredicates"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/api/configsync/v1beta1"
	"kpt.dev/configsync/pkg/applier"
	"kpt.dev/configsync/pkg/core"
	"kpt.dev/configsync/pkg/core/k8sobjects"
	"kpt.dev/configsync/pkg/declared"
	"kpt.dev/configsync/pkg/kinds"
	"kpt.dev/configsync/pkg/metadata"
	"kpt.dev/configsync/pkg/reconcilermanager"
	"sigs.k8s.io/cli-utils/pkg/common"
)

// TestNamespaceStrategy focuses on the behavior of switching between modes
// of namespaceStrategy for a single RootSync.
// - Set namespaceStrategy: explicit
// - Declare resource in "foo-implicit" Namespace but not the Namespace itself (should error)
// - Set namespaceStrategy: implicit - should create the Namespace implicitly
// - Set namespaceStrategy: explicit - Namespace still exists but is unmanaged
// - Declare namespace "foo-implicit" in git. The Namespace should be managed explicitly
// - Prune "foo-implicit" from git. Should error because cm1 depends on the Namespace
// - Prune "cm1" from git. The Namespace and ConfigMap should be successfully pruned.
func TestNamespaceStrategy(t *testing.T) {
	nt := nomostest.New(t, nomostesting.OverrideAPI, ntopts.Unstructured)
	rootSyncGitRepo := nt.SyncSourceGitRepository(nomostest.DefaultRootSyncID)

	rootSyncNN := nomostest.RootSyncNN(configsync.RootSyncName)
	rootReconcilerNN := core.RootReconcilerObjectKey(rootSyncNN.Name)
	rootSync := k8sobjects.RootSyncObjectV1Alpha1(rootSyncNN.Name)
	// set the NamespaceStrategy to explicit
	nt.MustMergePatch(rootSync, `{"spec": {"override": {"namespaceStrategy": "explicit"}}}`)
	err := nt.Watcher.WatchObject(
		kinds.Deployment(), rootReconcilerNN.Name, rootReconcilerNN.Namespace,
		[]testpredicates.Predicate{
			testpredicates.DeploymentHasEnvVar(
				reconcilermanager.Reconciler,
				reconcilermanager.NamespaceStrategy,
				string(configsync.NamespaceStrategyExplicit),
			),
		},
	)
	if err != nil {
		nt.T.Fatal(err)
	}
	// add a resource for which the namespace is not declared/created
	fooNamespace := k8sobjects.NamespaceObject("foo-implicit")
	cm1 := k8sobjects.ConfigMapObject(core.Name("cm1"), core.Namespace(fooNamespace.Name))
	nt.Must(rootSyncGitRepo.Add("acme/cm1.yaml", cm1))
	nt.Must(rootSyncGitRepo.CommitAndPush("Add cm1"))

	// check for error
	nt.WaitForRootSyncSyncError(rootSyncNN.Name, applier.ApplierErrorCode,
		"failed to apply ConfigMap, foo-implicit/cm1: namespaces \"foo-implicit\" not found", []v1beta1.ResourceRef{{
			SourcePath: "acme/cm1.yaml",
			Name:       "cm1",
			Namespace:  "foo-implicit",
			GVK: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "ConfigMap",
			},
		}})

	// switch the mode to implicit
	nt.MustMergePatch(rootSync, `{"spec": {"override": {"namespaceStrategy": "implicit"}}}`)
	// check for success
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}
	// assert that implicit namespace was created
	err = nt.Validate(fooNamespace.Name, fooNamespace.Namespace, &corev1.Namespace{},
		testpredicates.HasAnnotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
		testpredicates.HasAnnotation(metadata.ResourceManagementKey, metadata.ResourceManagementEnabled),
		testpredicates.HasAnnotation(metadata.ResourceManagerKey, string(declared.RootScope)),
	)
	if err != nil {
		nt.T.Fatal(err)
	}
	// switch mode back to explicit
	nt.MustMergePatch(rootSync, `{"spec": {"override": {"namespaceStrategy": "explicit"}}}`)
	// assert that namespace is no longer managed
	err = nt.Watcher.WatchObject(kinds.Namespace(), fooNamespace.Name, fooNamespace.Namespace,
		[]testpredicates.Predicate{
			// still has PreventDeletion
			testpredicates.HasAnnotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
			// management annotations should be removed
			testpredicates.MissingAnnotation(metadata.ResourceManagementKey),
			testpredicates.MissingAnnotation(metadata.ResourceManagerKey),
		},
	)
	if err != nil {
		nt.T.Fatal(err)
	}
	// explicitly declare the namespace in git
	nt.Must(rootSyncGitRepo.Add("acme/namespace-foo.yaml", fooNamespace))
	nt.Must(rootSyncGitRepo.CommitAndPush("Explicitly manage fooNamespace"))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}
	// assert that namespace is managed
	err = nt.Watcher.WatchObject(kinds.Namespace(), fooNamespace.Name, fooNamespace.Namespace,
		[]testpredicates.Predicate{
			// Config Sync uses a single field manager, so the PreventDeletion
			// annotation is removed.
			// Users can still declare the annotation in the explicit namespace.
			testpredicates.MissingAnnotation(common.LifecycleDeleteAnnotation),
			testpredicates.HasAnnotation(metadata.ResourceManagementKey, metadata.ResourceManagementEnabled),
			testpredicates.HasAnnotation(metadata.ResourceManagerKey, string(declared.RootScope)),
		},
	)
	if err != nil {
		nt.T.Fatal(err)
	}
	// prune the namespace
	nt.Must(rootSyncGitRepo.Remove("acme/namespace-foo.yaml"))
	nt.Must(rootSyncGitRepo.CommitAndPush("Prune namespace-foo"))
	// check for error
	nt.WaitForRootSyncSyncError(rootSyncNN.Name, applier.ApplierErrorCode,
		"skipped delete of Namespace, /foo-implicit: namespace still in use: foo-implicit", nil)
	// prune the ConfigMap
	nt.Must(rootSyncGitRepo.Remove("acme/cm1.yaml"))
	nt.Must(rootSyncGitRepo.CommitAndPush("Prune cm1"))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}
	// all resources should be pruned
	tg := taskgroup.New()
	tg.Go(func() error {
		return nt.Watcher.WatchForNotFound(kinds.Namespace(), fooNamespace.Name, fooNamespace.Namespace)
	})
	tg.Go(func() error {
		return nt.Watcher.WatchForNotFound(kinds.ConfigMap(), cm1.Name, cm1.Namespace)
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}
}

// TestNamespaceStrategyMultipleRootSyncs focuses on the behavior of using
// namespaceStrategy: explicit with multiple RootSyncs.
// When using multiple RootSyncs that declare resources in the same namespace,
// the namespace should only be created if declared explicitly in a sync source.
func TestNamespaceStrategyMultipleRootSyncs(t *testing.T) {
	rootSyncID := nomostest.DefaultRootSyncID
	rootSyncAID := core.RootSyncID("sync-a")
	rootSyncXID := core.RootSyncID("sync-x")
	rootSyncYID := core.RootSyncID("sync-y")
	namespaceA := k8sobjects.NamespaceObject("namespace-a")
	nt := nomostest.New(t, nomostesting.OverrideAPI, ntopts.Unstructured,
		ntopts.SyncWithGitSource(rootSyncAID), // will declare namespace-a explicitly
		ntopts.SyncWithGitSource(rootSyncXID), // will declare resources in namespace-a, but not namespace-a itself
		ntopts.SyncWithGitSource(rootSyncYID), // will declare resources in namespace-a, but not namespace-a itself
	)
	rootSyncGitRepo := nt.SyncSourceGitRepository(rootSyncID)
	rootSyncAGitRepo := nt.SyncSourceGitRepository(rootSyncAID)
	rootSyncXGitRepo := nt.SyncSourceGitRepository(rootSyncXID)
	rootSyncYGitRepo := nt.SyncSourceGitRepository(rootSyncYID)

	rootSyncA := nomostest.RootSyncObjectV1Beta1FromRootRepo(nt, rootSyncAID.Name)
	rootSyncX := nomostest.RootSyncObjectV1Beta1FromRootRepo(nt, rootSyncXID.Name)
	rootSyncY := nomostest.RootSyncObjectV1Beta1FromRootRepo(nt, rootSyncYID.Name)
	rootSyncA.Spec.SafeOverride().NamespaceStrategy = configsync.NamespaceStrategyExplicit
	rootSyncX.Spec.SafeOverride().NamespaceStrategy = configsync.NamespaceStrategyExplicit
	rootSyncY.Spec.SafeOverride().NamespaceStrategy = configsync.NamespaceStrategyExplicit

	// set the NamespaceStrategy to explicit
	nt.Must(rootSyncGitRepo.Add(
		fmt.Sprintf("acme/namespaces/%s/%s.yaml", configsync.ControllerNamespace, rootSyncA.Name),
		rootSyncA,
	))
	nt.Must(rootSyncGitRepo.Add(
		fmt.Sprintf("acme/namespaces/%s/%s.yaml", configsync.ControllerNamespace, rootSyncX.Name),
		rootSyncX,
	))
	nt.Must(rootSyncGitRepo.Add(
		fmt.Sprintf("acme/namespaces/%s/%s.yaml", configsync.ControllerNamespace, rootSyncY.Name),
		rootSyncY,
	))
	nt.Must(rootSyncGitRepo.CommitAndPush(
		fmt.Sprintf("Adding RootSyncs (%s, %s, %s) with namespaceStrategy=explicit",
			rootSyncA.Name, rootSyncX.Name, rootSyncY.Name),
	))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}
	// Assert that all reconcilers have NAMESPACE_STRATEGY=explicit set
	tg := taskgroup.New()
	for _, rsName := range []string{rootSyncA.Name, rootSyncX.Name, rootSyncY.Name} {
		reconcilerNN := core.RootReconcilerObjectKey(rsName)
		tg.Go(func() error {
			return nt.Watcher.WatchObject(
				kinds.Deployment(), reconcilerNN.Name, reconcilerNN.Namespace,
				[]testpredicates.Predicate{
					testpredicates.DeploymentHasEnvVar(
						reconcilermanager.Reconciler,
						reconcilermanager.NamespaceStrategy,
						string(configsync.NamespaceStrategyExplicit),
					),
				},
			)
		})
	}
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}
	// add a resource for which the namespace is not declared/created
	cmX := k8sobjects.ConfigMapObject(core.Name("cm-x"), core.Namespace(namespaceA.Name))
	nt.Must(rootSyncXGitRepo.Add("acme/cm-x.yaml", cmX))
	nt.Must(rootSyncXGitRepo.CommitAndPush("Add cm-x"))
	cmY := k8sobjects.ConfigMapObject(core.Name("cm-y"), core.Namespace(namespaceA.Name))
	nt.Must(rootSyncYGitRepo.Add("acme/cm-y.yaml", cmY))
	nt.Must(rootSyncYGitRepo.CommitAndPush("Add cm-y"))
	// check for error
	nt.WaitForRootSyncSyncError(rootSyncX.Name, applier.ApplierErrorCode,
		"failed to apply ConfigMap, namespace-a/cm-x: namespaces \"namespace-a\" not found", nil)
	nt.WaitForRootSyncSyncError(rootSyncY.Name, applier.ApplierErrorCode,
		"failed to apply ConfigMap, namespace-a/cm-y: namespaces \"namespace-a\" not found", nil)
	// declare the namespace in sync-a
	nt.Must(rootSyncAGitRepo.Add("acme/namespace-a.yaml", namespaceA))
	nt.Must(rootSyncAGitRepo.CommitAndPush("Add namespace-a"))
	// check for success
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}
	// assert that all resources were created
	tg = taskgroup.New()
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.Namespace(), namespaceA.Name, namespaceA.Namespace,
			[]testpredicates.Predicate{
				// Users can add PreventDeletion annotation to the declared namespace
				// if they choose, but the reconciler does not add it by default.
				testpredicates.MissingAnnotation(common.LifecycleDeleteAnnotation),
				testpredicates.HasAnnotation(metadata.ResourceManagementKey, metadata.ResourceManagementEnabled),
				testpredicates.HasAnnotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, rootSyncA.Name)),
			})
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.ConfigMap(), cmX.Name, cmX.Namespace,
			[]testpredicates.Predicate{
				testpredicates.HasAnnotation(metadata.ResourceManagementKey, metadata.ResourceManagementEnabled),
				testpredicates.HasAnnotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, rootSyncX.Name)),
			})
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.ConfigMap(), cmY.Name, cmY.Namespace,
			[]testpredicates.Predicate{
				testpredicates.HasAnnotation(metadata.ResourceManagementKey, metadata.ResourceManagementEnabled),
				testpredicates.HasAnnotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, rootSyncY.Name)),
			})
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}
}
