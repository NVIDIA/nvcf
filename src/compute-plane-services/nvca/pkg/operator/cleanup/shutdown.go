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

package cleanup

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
)

// ShutdownResponse is the JSON response from the /shutdown endpoint
type ShutdownResponse struct {
	Cleanup bool   `json:"cleanup"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// ShutdownHandlerOptions contains the dependencies for the shutdown handler
type ShutdownHandlerOptions struct {
	K8sClient      kubernetes.Interface
	NVCAClient     versioned.Interface
	DynamicClient  dynamic.Interface
	Namespace      string
	PollTimeout    time.Duration             // Max time to poll for deletion signals (default: 15s)
	DrainTimeout   time.Duration             // Max time to wait for workloads to drain (default: 5m)
	RolloutTimeout time.Duration             // Max time to wait for NVCA deployment rollout (default: 2m)
	OnShutdown     func(ctx context.Context) // Called when shutdown starts (e.g., to stop event processing)
	// SetGracefulShutdown is called to signal that the operator is in graceful shutdown mode.
	// When set to true, the reconciliation loop will skip cleanup of NVCFBackend resources
	// and let the shutdown handler manage the cleanup instead.
	SetGracefulShutdown func(shutdown bool)

	// RBAC resource names for finalizer cleanup
	ClusterRoleName        string // Name of the operator's ClusterRole
	ClusterRoleBindingName string // Name of the operator's ClusterRoleBinding
	ServiceAccountName     string // Name of the operator's ServiceAccount
}

// NewShutdownHandler returns an HTTP handler for the /shutdown endpoint.
// This is called by the preStop hook to perform graceful cleanup.
func NewShutdownHandler(ctx context.Context, opts ShutdownHandlerOptions) http.HandlerFunc {
	// Calculate polling constants once at handler creation
	const pollInterval = 2 * time.Second
	maxWait := opts.PollTimeout
	if maxWait == 0 {
		maxWait = 15 * time.Second
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		log := core.GetLogger(ctx)
		w.Header().Set("Content-Type", "application/json")

		respond := func(statusCode int, resp ShutdownResponse) {
			w.WriteHeader(statusCode)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				log.WithError(err).Error("Failed to encode shutdown response")
			}
		}

		log.Info("Shutdown endpoint called, starting deletion detection polling")

		// Set graceful shutdown flag IMMEDIATELY when the preStop hook calls us.
		// This prevents any reconciliation from running cleanup before we're ready.
		// We set this before polling because the NVCFBackend may already have a
		// deletion timestamp, and reconciliation could act on it while we're polling.
		if opts.SetGracefulShutdown != nil {
			opts.SetGracefulShutdown(true)
			log.Info("Graceful shutdown flag set, reconciliation cleanup is now deferred")
		}

		start := time.Now()
		shouldCleanup := false

		for time.Since(start) < maxWait {
			elapsed := time.Since(start).Round(time.Millisecond)

			// Check if sentinel is being deleted
			sentinelDeleting, sentinelErr := IsSentinelBeingDeleted(ctx, opts.K8sClient, opts.Namespace, ShutdownSentinelConfigMapName)
			if sentinelErr != nil {
				log.WithError(sentinelErr).Warn("Failed to check sentinel status")
			}

			log.Infof("Shutdown poll: sentinel_deleting=%v, elapsed=%v", sentinelDeleting, elapsed)

			if sentinelDeleting {
				shouldCleanup = true
				break
			}

			time.Sleep(pollInterval)
		}

		if !shouldCleanup {
			log.Info("No deletion detected after polling, assuming normal restart")
			// Reset the graceful shutdown flag since we're not actually shutting down
			if opts.SetGracefulShutdown != nil {
				opts.SetGracefulShutdown(false)
			}
			respond(http.StatusOK, ShutdownResponse{
				Cleanup: false,
				Message: "no deletion detected, normal restart",
			})
			return
		}

		log.Info("Deletion detected, starting cleanup...")

		resp := RunShutdownCleanup(ctx, opts)
		statusCode := http.StatusOK
		if resp.Error != "" {
			statusCode = http.StatusInternalServerError
		}
		respond(statusCode, resp)
	}
}

// RunShutdownCleanup performs the operator-managed cleanup used by both the
// preStop /shutdown handler and the Helm pre-delete cleanup job.
func RunShutdownCleanup(ctx context.Context, opts ShutdownHandlerOptions) ShutdownResponse {
	log := core.GetLogger(ctx)
	drainTimeout := opts.DrainTimeout
	if drainTimeout == 0 {
		drainTimeout = 6 * time.Minute
	}
	rolloutTimeout := opts.RolloutTimeout
	if rolloutTimeout == 0 {
		rolloutTimeout = 2 * time.Minute
	}

	// Call the shutdown callback (e.g., to stop event processing)
	if opts.OnShutdown != nil {
		opts.OnShutdown(ctx)
	}

	if err := DeleteShutdownSentinel(ctx, opts.K8sClient, opts.Namespace); err != nil {
		log.WithError(err).Warn("Failed to mark shutdown sentinel for deletion, continuing with cleanup")
	}

	// Get the NVCFBackend (there can only be one per cluster)
	backends, err := opts.NVCAClient.NvcfV1().NVCFBackends(opts.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to list NVCFBackends")
		return ShutdownResponse{
			Cleanup: false,
			Message: "failed to list NVCFBackends",
			Error:   err.Error(),
		}
	}

	if len(backends.Items) == 0 {
		log.Info("No NVCFBackend to clean up")
	} else {
		if len(backends.Items) > 1 {
			log.Warnf("Found %d NVCFBackends, expected 1. Processing only the first: %s/%s",
				len(backends.Items), backends.Items[0].Namespace, backends.Items[0].Name)
		}
		nb := &backends.Items[0]
		log.Infof("Processing NVCFBackend %s/%s", nb.Namespace, nb.Name)

		// Get namespaces for this backend
		systemNS, requestsNS := BackendNamespaces(nb)

		// Check if there are any ICMSRequest CRs (active workloads)
		requestCount, err := CountICMSRequests(ctx, opts.DynamicClient, requestsNS)
		if err != nil {
			log.WithError(err).Warnf("Failed to count ICMS requests in namespace %s, proceeding with cleanup", requestsNS)
		} else if requestCount > 0 {
			drainWorkloads(ctx, opts.K8sClient, opts.DynamicClient, systemNS, requestsNS, requestCount, rolloutTimeout, drainTimeout)
		}

		log.Infof("Cleaning up NVCFBackend %s/%s", nb.Namespace, nb.Name)

		// Mark for deletion (sets deletionTimestamp; finalizer prevents actual removal until explicitly cleared below)
		if err := DeleteNVCFBackend(ctx, opts.NVCAClient, nb); err != nil {
			log.WithError(err).Errorf("Failed to delete NVCFBackend %s/%s", nb.Namespace, nb.Name)
			// Continue with cleanup anyway
		}

		// Use shared cleanup to delete all managed resources
		if err := CleanupBackendResources(ctx, opts.K8sClient, opts.DynamicClient, nb); err != nil {
			log.WithError(err).Errorf("Failed to cleanup resources for NVCFBackend %s/%s", nb.Namespace, nb.Name)
		}

		// Remove the finalizer from the NVCFBackend to allow it to be garbage collected
		if err := RemoveNVCFBackendFinalizer(ctx, opts.NVCAClient, nb); err != nil {
			log.WithError(err).Errorf("Failed to remove finalizer from NVCFBackend %s/%s", nb.Namespace, nb.Name)
		}

		log.Infof("Cleaned up NVCFBackend %s/%s", nb.Namespace, nb.Name)
	}

	// Note: Operator-managed CRDs (ICMSRequest, StorageRequest, MiniServices) have owner references
	// to the NVCFBackend CRD, so they will be garbage collected when Helm deletes that CRD.

	// Remove finalizers from RBAC resources (ClusterRole, ClusterRoleBinding, ServiceAccount)
	// This must be done before removing the sentinel finalizer to ensure the operator retains permissions
	if opts.ClusterRoleName != "" || opts.ClusterRoleBindingName != "" || opts.ServiceAccountName != "" {
		log.Info("Removing finalizers from RBAC resources")
		if err := RemoveRBACFinalizers(ctx, opts.K8sClient,
			opts.ClusterRoleName,
			opts.ClusterRoleBindingName,
			opts.ServiceAccountName,
			opts.Namespace, // ServiceAccount namespace is same as operator namespace
			SentinelFinalizer,
		); err != nil {
			log.WithError(err).Warn("Failed to remove some RBAC finalizers, continuing with sentinel cleanup")
			// Continue with sentinel cleanup even if RBAC cleanup has issues
		}
	}

	// Remove finalizer from sentinel ConfigMap
	log.Infof("Removing finalizer from sentinel ConfigMap %s/%s", opts.Namespace, ShutdownSentinelConfigMapName)
	if err := RemoveSentinelFinalizer(ctx, opts.K8sClient, opts.Namespace, ShutdownSentinelConfigMapName, SentinelFinalizer); err != nil {
		log.WithError(err).Error("Failed to remove sentinel finalizer")
		return ShutdownResponse{
			Cleanup: true,
			Message: "cleanup completed but failed to remove sentinel finalizer",
			Error:   err.Error(),
		}
	}

	log.Info("Cleanup completed successfully")
	return ShutdownResponse{
		Cleanup: true,
		Message: "cleanup complete",
	}
}

// DeleteShutdownSentinel marks the shutdown sentinel ConfigMap for deletion.
// This lets other running operator loops observe shutdown and stop processing
// while the cleanup flow removes managed resources.
func DeleteShutdownSentinel(ctx context.Context, k8sClient kubernetes.Interface, namespace string) error {
	err := k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, ShutdownSentinelConfigMapName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

// drainWorkloads enables NVCA cordon-and-drain maintenance mode and waits for
// active workloads (ICMSRequest CRs) to terminate before returning.
func drainWorkloads(ctx context.Context, k8sClient kubernetes.Interface, dynamicClient dynamic.Interface,
	systemNS, requestsNS string, requestCount int, rolloutTimeout, drainTimeout time.Duration) {
	const drainPollInterval = 5 * time.Second
	log := core.GetLogger(ctx)

	log.Infof("Found %d ICMS request(s) in namespace %s, enabling cordon and drain mode", requestCount, requestsNS)

	// Directly patch the NVCA agent-config ConfigMap with maintenanceMode.
	// This avoids full reconciliation which can fail during shutdown when
	// other helm-managed resources are already deleted.
	if err := PatchNVCAMaintenanceMode(ctx, k8sClient, systemNS, string(nvcaconfig.MaintenanceModeCordonAndDrain)); err != nil {
		log.WithError(err).Warn("Failed to patch NVCA config with maintenance mode, NVCA may not drain workloads")
	} else {
		log.Infof("Waiting up to %v for NVCA deployment rollout in namespace %s", rolloutTimeout, systemNS)
		if err := waitForDeploymentRollout(ctx, k8sClient, systemNS, NVCAModuleName, rolloutTimeout); err != nil {
			log.WithError(err).Warn("NVCA deployment rollout did not complete in time, continuing with drain wait")
		} else {
			log.Info("NVCA deployment rollout completed")
		}
	}

	log.Infof("Waiting up to %v for workloads to drain...", drainTimeout)
	drainStart := time.Now()
	for time.Since(drainStart) < drainTimeout {
		remaining, err := CountICMSRequests(ctx, dynamicClient, requestsNS)
		if err != nil {
			log.WithError(err).Warn("Failed to count ICMS requests during drain wait")
			break
		}
		if remaining == 0 {
			log.Info("All workloads drained successfully")
			return
		}
		log.Infof("Waiting for %d ICMS request(s) to drain (elapsed: %v)", remaining, time.Since(drainStart).Round(time.Second))
		time.Sleep(drainPollInterval)
	}

	finalCount, _ := CountICMSRequests(ctx, dynamicClient, requestsNS)
	if finalCount > 0 {
		log.Warnf("Drain timeout reached with %d ICMS request(s) remaining, proceeding with forced cleanup", finalCount)
	}
}
