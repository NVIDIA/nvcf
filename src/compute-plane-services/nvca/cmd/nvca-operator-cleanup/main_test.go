/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

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

package main

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cli "github.com/urfave/cli/v2"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	fakenvcaop "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
)

func TestRunCleanup_ReturnsInvalidLogLevelBeforeUsingCluster(t *testing.T) {
	ctx := cleanupCLIContext(t, "--log-level", "not-a-log-level")

	err := runCleanup(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-log-level")
}

func TestDefaultCleanupDeps_AreConfigured(t *testing.T) {
	deps := defaultCleanupDeps()

	assert.NotNil(t, deps.kubernetesConfig)
	assert.NotNil(t, deps.newK8sClient)
	assert.NotNil(t, deps.newNVCAClient)
	assert.NotNil(t, deps.newDynamicClient)
	assert.NotNil(t, deps.runShutdownCleanup)
}

func TestDefaultCleanupDeps_ClientFactoriesBuildClients(t *testing.T) {
	deps := defaultCleanupDeps()
	cfg := &rest.Config{Host: "https://example.invalid"}

	k8sClient, err := deps.newK8sClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, k8sClient)

	nvcaClient, err := deps.newNVCAClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, nvcaClient)

	dynamicClient, err := deps.newDynamicClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, dynamicClient)
}

func TestKubernetesConfig_ReturnsErrorForMissingExplicitKubeconfig(t *testing.T) {
	_, err := kubernetesConfig(filepath.Join(t.TempDir(), "missing-kubeconfig"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-kubeconfig")
}

func TestRunCleanupWithDeps_InvokesSharedCleanupWithCLIOptions(t *testing.T) {
	ctx := cleanupCLIContext(t,
		"--namespace", "custom-namespace",
		"--kubeconfig", "/tmp/test-kubeconfig",
		"--log-level", "debug",
		"--drain-timeout", "9s",
		"--rollout-timeout", "7s",
		"--cluster-role-name", "custom-cluster-role",
		"--cluster-role-binding-name", "custom-cluster-role-binding",
		"--service-account-name", "custom-service-account",
	)

	var gotKubeconfig string
	var gotOpts cleanup.ShutdownHandlerOptions
	deps := fakeCleanupDeps(t)
	deps.kubernetesConfig = func(kubeconfig string) (*rest.Config, error) {
		gotKubeconfig = kubeconfig
		return &rest.Config{Host: "https://example.invalid"}, nil
	}
	deps.runShutdownCleanup = func(_ context.Context, opts cleanup.ShutdownHandlerOptions) cleanup.ShutdownResponse {
		gotOpts = opts
		return cleanup.ShutdownResponse{Cleanup: true, Message: "cleanup complete"}
	}

	require.NoError(t, runCleanupWithDeps(ctx, deps))
	assert.Equal(t, "/tmp/test-kubeconfig", gotKubeconfig)
	assert.Equal(t, "custom-namespace", gotOpts.Namespace)
	assert.Equal(t, 9*time.Second, gotOpts.DrainTimeout)
	assert.Equal(t, 7*time.Second, gotOpts.RolloutTimeout)
	assert.Equal(t, "custom-cluster-role", gotOpts.ClusterRoleName)
	assert.Equal(t, "custom-cluster-role-binding", gotOpts.ClusterRoleBindingName)
	assert.Equal(t, "custom-service-account", gotOpts.ServiceAccountName)
	assert.NotNil(t, gotOpts.K8sClient)
	assert.NotNil(t, gotOpts.NVCAClient)
	assert.NotNil(t, gotOpts.DynamicClient)
}

func TestRunCleanupWithDeps_ReturnsCleanupError(t *testing.T) {
	ctx := cleanupCLIContext(t)
	deps := fakeCleanupDeps(t)
	deps.runShutdownCleanup = func(context.Context, cleanup.ShutdownHandlerOptions) cleanup.ShutdownResponse {
		return cleanup.ShutdownResponse{
			Cleanup: true,
			Message: "cleanup failed",
			Error:   "sentinel finalizer is still present",
		}
	}

	err := runCleanupWithDeps(ctx, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.Contains(t, err.Error(), "sentinel finalizer is still present")
}

func TestRunCleanupWithDeps_PreservesHookClusterRoleBindingAfterCleanupError(t *testing.T) {
	ctx := cleanupCLIContext(t, "--hook-cluster-role-binding-name", "hook-cleanup")
	k8sClient := fake.NewSimpleClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "hook-cleanup"},
	})
	deps := fakeCleanupDeps(t)
	deps.newK8sClient = func(*rest.Config) (kubernetes.Interface, error) {
		return k8sClient, nil
	}
	deps.runShutdownCleanup = func(context.Context, cleanup.ShutdownHandlerOptions) cleanup.ShutdownResponse {
		return cleanup.ShutdownResponse{
			Cleanup: true,
			Message: "cleanup failed",
			Error:   "sentinel finalizer is still present",
		}
	}

	require.Error(t, runCleanupWithDeps(ctx, deps))

	_, err := k8sClient.RbacV1().ClusterRoleBindings().Get(context.Background(), "hook-cleanup", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestRunCleanupWithDeps_RemovesHookClusterRoleBindingAfterSuccessfulCleanup(t *testing.T) {
	ctx := cleanupCLIContext(t, "--hook-cluster-role-binding-name", "hook-cleanup")
	k8sClient := fake.NewSimpleClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "hook-cleanup"},
	})
	deps := fakeCleanupDeps(t)
	deps.newK8sClient = func(*rest.Config) (kubernetes.Interface, error) {
		return k8sClient, nil
	}

	require.NoError(t, runCleanupWithDeps(ctx, deps))

	_, err := k8sClient.RbacV1().ClusterRoleBindings().Get(context.Background(), "hook-cleanup", metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, k8serrors.IsNotFound(err), "hook ClusterRoleBinding should be removed last")
}

func TestRunCleanupWithDeps_ReturnsSetupErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*cleanupDeps)
		wantErr string
	}{
		{
			name: "kubeconfig",
			mutate: func(deps *cleanupDeps) {
				deps.kubernetesConfig = func(string) (*rest.Config, error) {
					return nil, errors.New("load kubeconfig")
				}
			},
			wantErr: "load kubeconfig",
		},
		{
			name: "kubernetes client",
			mutate: func(deps *cleanupDeps) {
				deps.newK8sClient = func(*rest.Config) (kubernetes.Interface, error) {
					return nil, errors.New("k8s client")
				}
			},
			wantErr: "create kubernetes client: k8s client",
		},
		{
			name: "nvca client",
			mutate: func(deps *cleanupDeps) {
				deps.newNVCAClient = func(*rest.Config) (versioned.Interface, error) {
					return nil, errors.New("nvca client")
				}
			},
			wantErr: "create nvca client: nvca client",
		},
		{
			name: "dynamic client",
			mutate: func(deps *cleanupDeps) {
				deps.newDynamicClient = func(*rest.Config) (dynamic.Interface, error) {
					return nil, errors.New("dynamic client")
				}
			},
			wantErr: "create dynamic client: dynamic client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := cleanupCLIContext(t)
			deps := fakeCleanupDeps(t)
			tt.mutate(&deps)

			err := runCleanupWithDeps(ctx, deps)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func cleanupCLIContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	app := newCleanupApp()
	set := flag.NewFlagSet("nvca-operator-cleanup-test", flag.ContinueOnError)
	for _, cliFlag := range app.Flags {
		require.NoError(t, cliFlag.Apply(set))
	}
	require.NoError(t, set.Parse(args))
	return cli.NewContext(app, set, nil)
}

func fakeCleanupDeps(t *testing.T) cleanupDeps {
	t.Helper()
	scheme := runtime.NewScheme()
	return cleanupDeps{
		kubernetesConfig: func(string) (*rest.Config, error) {
			return &rest.Config{Host: "https://example.invalid"}, nil
		},
		newK8sClient: func(*rest.Config) (kubernetes.Interface, error) {
			return fake.NewSimpleClientset(), nil
		},
		newNVCAClient: func(*rest.Config) (versioned.Interface, error) {
			return fakenvcaop.NewSimpleClientset(), nil
		},
		newDynamicClient: func(*rest.Config) (dynamic.Interface, error) {
			return fakedynamic.NewSimpleDynamicClient(scheme), nil
		},
		runShutdownCleanup: func(context.Context, cleanup.ShutdownHandlerOptions) cleanup.ShutdownResponse {
			return cleanup.ShutdownResponse{Cleanup: true, Message: "cleanup complete"}
		},
	}
}
