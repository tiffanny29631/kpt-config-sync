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
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/ntopts"
	"kpt.dev/configsync/e2e/nomostest/policy"
	"kpt.dev/configsync/e2e/nomostest/taskgroup"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/e2e/nomostest/testpredicates"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/core"
	"kpt.dev/configsync/pkg/core/k8sobjects"
	"kpt.dev/configsync/pkg/kinds"
	"kpt.dev/configsync/pkg/metadata"
	"kpt.dev/configsync/pkg/reconcilermanager"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
)

// This file includes tests for drift correction and drift prevention.
//
// The drift prevention is only supported in the multi-repo mode, and utilizes the following Config Sync metadata:
//  * the configmanagement.gke.io/managed annotation
//  * the configsync.gke.io/resource-id annotation
//  * the configsync.gke.io/declared-version label

func TestAdmission(t *testing.T) {
	nt := nomostest.New(t, nomostesting.DriftControl)

	rootSyncGitRepo := nt.SyncSourceGitRepository(nomostest.DefaultRootSyncID)

	nt.Must(rootSyncGitRepo.Add("acme/namespaces/hello/ns.yaml",
		k8sobjects.NamespaceObject("hello", core.Annotation("goodbye", "moon"))))
	nt.Must(rootSyncGitRepo.CommitAndPush("add Namespace"))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}

	// Ensure we properly forbid changing declared information.

	nomostest.WaitForWebhookReadiness(nt)

	// Prevent deleting declared objects.
	_, err := nt.Shell.Kubectl("delete", "ns", "hello")
	if err == nil {
		nt.T.Fatal("got `kubectl delete ns hello` success, want return err")
	}

	// Prevent changing declared data.
	_, err = nt.Shell.Kubectl("annotate", "--overwrite", "ns", "hello", "goodbye=world")
	if err == nil {
		nt.T.Fatal("got `kubectl annotate --overwrite ns hello goodbye=world` success, want return err")
	}

	// Prevent removing declared data from declared objects.
	_, err = nt.Shell.Kubectl("annotate", "ns", "hello", "goodbye-")
	if err == nil {
		nt.T.Fatal("got `kubectl annotate ns hello goodbye-` success, want return err")
	}

	// Ensure we allow changing information which is not declared.

	// Allow adding data in declared objects.
	out, err := nt.Shell.Kubectl("annotate", "ns", "hello", "stop=go")
	if err != nil {
		nt.T.Fatalf("got `kubectl annotate ns hello stop=go` error %v %s, want return nil", err, out)
	}

	// Allow changing non-declared data in declared objects.
	out, err = nt.Shell.Kubectl("annotate", "--overwrite", "ns", "hello", "stop='oh no'")
	if err != nil {
		nt.T.Fatalf("got `kubectl annotate --overwrite ns hello stop='oh no'` error %v %s, want return nil", err, out)
	}

	// Allow reing non-declared data in declared objects.
	out, err = nt.Shell.Kubectl("annotate", "ns", "hello", "stop-")
	if err != nil {
		nt.T.Fatalf("got `kubectl annotate ns hello stop-` error %v %s, want return nil", err, out)
	}

	// Prevent creating a managed resource.
	ns := []byte(`
apiVersion: v1
kind: Namespace
metadata:
  annotations:
    configmanagement.gke.io/managed: enabled
    configsync.gke.io/resource-id: _namespace_test-ns
  labels:
    configsync.gke.io/declared-version: v1
  name: test-ns
`)

	if err := os.WriteFile(filepath.Join(nt.TmpDir, "test-ns.yaml"), ns, 0644); err != nil {
		nt.T.Fatalf("failed to create a tmp file %v", err)
	}

	_, err = nt.Shell.Kubectl("apply", "-f", filepath.Join(nt.TmpDir, "test-ns.yaml"))
	if err == nil {
		nt.T.Fatal("got `kubectl apply -f test-ns.yaml` success, want return err")
	}

	// Allow creating/deleting a resource whose `configsync.gke.io/resource-id` does not match the resource,
	// but whose `configmanagement.gke.io/managed` annotation is `enabled` and whose
	// `configsync.gke.io/declared-version` label is `v1`.
	//
	// The remediator will not remove the Nomos metadata from `test-ns`, since `test-ns` is
	// not a managed resource.
	ns = []byte(`
apiVersion: v1
kind: Namespace
metadata:
  annotations:
    configmanagement.gke.io/managed: enabled
    configsync.gke.io/resource-id: _namespace_wrong-ns
  labels:
    configsync.gke.io/declared-version: v1
  name: test-ns
`)

	if err := os.WriteFile(filepath.Join(nt.TmpDir, "test-ns.yaml"), ns, 0644); err != nil {
		nt.T.Fatalf("failed to create a tmp file %v", err)
	}

	out, err = nt.Shell.Kubectl("apply", "-f", filepath.Join(nt.TmpDir, "test-ns.yaml"))
	if err != nil {
		nt.T.Fatalf("got `kubectl apply -f test-ns.yaml` error %v %s, want return nil", err, out)
	}

	out, err = nt.Shell.Kubectl("delete", "-f", filepath.Join(nt.TmpDir, "test-ns.yaml"))
	if err != nil {
		nt.T.Fatalf("got `kubectl delete -f test-ns.yaml` error %v %s, want return nil", err, out)
	}
}

func TestDisableWebhookConfigurationUpdateHierarchy(t *testing.T) {
	nt := nomostest.New(t, nomostesting.DriftControl)

	// Test starts with Admission Webhook already installed
	nomostest.WaitForWebhookReadiness(nt)

	rootSyncGitRepo := nt.SyncSourceGitRepository(nomostest.DefaultRootSyncID)

	nt.Must(rootSyncGitRepo.Add("acme/namespaces/hello/ns.yaml", k8sobjects.NamespaceObject("hello")))
	nt.Must(rootSyncGitRepo.CommitAndPush("add test namespace"))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}

	err := nt.Validate("hello", "", &corev1.Namespace{}, testpredicates.HasAnnotationKey(metadata.DeclaredFieldsKey))
	if err != nil {
		nt.T.Fatal(err)
	}

	nomostest.StopWebhook(nt)

	tg := taskgroup.New()
	tg.Go(func() error {
		predicates := []testpredicates.Predicate{
			testpredicates.StatusEquals(nt.Scheme, kstatus.CurrentStatus),
			testpredicates.DeploymentMissingEnvVar(reconcilermanager.Reconciler, reconcilermanager.WebhookEnabled),
		}
		return nt.Watcher.WatchObject(kinds.Deployment(),
			core.RootReconcilerName(configsync.RootSyncName), configsync.ControllerNamespace, predicates)
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.Namespace(), "hello", "",
			[]testpredicates.Predicate{
				testpredicates.MissingAnnotation(metadata.DeclaredFieldsKey),
			})
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}

	// The object should be deleted and restored by Remediator
	nt.T.Log("Verify that the webhook is disabled")
	if err := nt.KubeClient.Delete(k8sobjects.Namespace("hello")); err != nil {
		nt.T.Fatalf("failed to run `kubectl delete ns hello` %v", err)
	}

	if err := nomostest.InstallWebhook(nt); err != nil {
		nt.T.Fatal(err)
	}
	nomostest.WaitForWebhookReadiness(nt)

	nt.T.Logf("Check declared-fields annotation is re-populated")
	tg = taskgroup.New()
	tg.Go(func() error {
		predicates := []testpredicates.Predicate{
			testpredicates.StatusEquals(nt.Scheme, kstatus.CurrentStatus),
			testpredicates.DeploymentHasEnvVar(reconcilermanager.Reconciler, reconcilermanager.WebhookEnabled, "true"),
		}
		return nt.Watcher.WatchObject(kinds.Deployment(),
			core.RootReconcilerName(configsync.RootSyncName), configsync.ControllerNamespace, predicates)
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.Namespace(), "hello", "",
			[]testpredicates.Predicate{
				testpredicates.HasAnnotationKey(metadata.DeclaredFieldsKey),
			})
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}
}

func TestDisableWebhookConfigurationUpdateUnstructured(t *testing.T) {
	repoSyncID := core.RepoSyncID(configsync.RepoSyncName, namespaceRepo)
	nt := nomostest.New(t, nomostesting.SyncSource,
		ntopts.SyncWithGitSource(repoSyncID),
		ntopts.RepoSyncPermissions(policy.CoreAdmin()))
	repoSyncGitRepo := nt.SyncSourceGitRepository(repoSyncID)

	sa := k8sobjects.ServiceAccountObject("store", core.Namespace(namespaceRepo))
	nt.Must(repoSyncGitRepo.Add("acme/sa.yaml", sa))
	nt.Must(repoSyncGitRepo.CommitAndPush("Adding test service account"))
	if err := nt.WatchForAllSyncs(); err != nil {
		nt.T.Fatal(err)
	}

	// Test starts with Admission Webhook already installed
	nomostest.WaitForWebhookReadiness(nt)

	err := nt.Validate("store", namespaceRepo, &corev1.ServiceAccount{}, testpredicates.HasAnnotationKey(metadata.DeclaredFieldsKey))
	if err != nil {
		nt.T.Fatal(err)
	}

	nomostest.StopWebhook(nt)

	tg := taskgroup.New()
	tg.Go(func() error {
		predicates := []testpredicates.Predicate{
			testpredicates.StatusEquals(nt.Scheme, kstatus.CurrentStatus),
			testpredicates.DeploymentMissingEnvVar(reconcilermanager.Reconciler, reconcilermanager.WebhookEnabled),
		}
		return nt.Watcher.WatchObject(kinds.Deployment(),
			core.NsReconcilerName(namespaceRepo, configsync.RepoSyncName), configsync.ControllerNamespace, predicates)
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.ServiceAccount(), "store", namespaceRepo,
			[]testpredicates.Predicate{
				testpredicates.MissingAnnotation(metadata.DeclaredFieldsKey),
			})
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}

	// The object should be deleted and restored by Remediator
	nt.T.Log("Verify that the webhook is disabled")
	if err := nt.KubeClient.Delete(sa); err != nil {
		nt.T.Fatalf("failed to remove objects from reposync %v", err)
	}

	if err := nomostest.InstallWebhook(nt); err != nil {
		nt.T.Fatal(err)
	}
	nomostest.WaitForWebhookReadiness(nt)

	nt.T.Logf("Check declared-fields annotation is re-populated")
	tg = taskgroup.New()
	tg.Go(func() error {
		predicates := []testpredicates.Predicate{
			testpredicates.StatusEquals(nt.Scheme, kstatus.CurrentStatus),
			testpredicates.DeploymentHasEnvVar(reconcilermanager.Reconciler, reconcilermanager.WebhookEnabled, "true"),
		}
		return nt.Watcher.WatchObject(kinds.Deployment(),
			core.NsReconcilerName(namespaceRepo, configsync.RepoSyncName), configsync.ControllerNamespace, predicates)
	})
	tg.Go(func() error {
		return nt.Watcher.WatchObject(kinds.ServiceAccount(), "store", namespaceRepo,
			[]testpredicates.Predicate{
				testpredicates.HasAnnotationKey(metadata.DeclaredFieldsKey),
			})
	})
	if err := tg.Wait(); err != nil {
		nt.T.Fatal(err)
	}
}
