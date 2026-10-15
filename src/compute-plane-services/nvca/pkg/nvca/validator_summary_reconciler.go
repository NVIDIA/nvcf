/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nvca

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

const (
	// defaultValidatorSummaryNamespace is the fallback when the operator
	// did not inject SummaryConfigMapNamespaceEnv (e.g. an older operator,
	// or a hand-rolled agent pod). Matches the Helm chart's default
	// install namespace, where the cluster-validator runs in the standard
	// topology.
	defaultValidatorSummaryNamespace = "nvca-operator"
)

// resolveValidatorSummaryNamespace returns the namespace the agent should
// watch for the cluster-validator summary ConfigMap. This is the
// operator/validator namespace — NOT the agent's own namespace, which
// differs in the standard split topology (validator runs in the operator
// namespace; the agent runs in the system namespace).
//
// The operator injects SummaryConfigMapNamespaceEnv with its own
// namespace when it constructs the agent Deployment; that value wins.
// We deliberately do NOT consult the agent's in-pod ServiceAccount mount
// (it resolves to the agent's own namespace, which is the wrong one) nor
// POD_NAMESPACE (which conventionally also means "my own namespace" and
// would silently reintroduce the namespace-mismatch bug if ever set on
// the agent).
func resolveValidatorSummaryNamespace() string {
	if ns := strings.TrimSpace(os.Getenv(clustervalidator.SummaryConfigMapNamespaceEnv)); ns != "" {
		return ns
	}
	return defaultValidatorSummaryNamespace
}

// ValidatorSummaryReconciler watches the well-known cluster-validator
// summary ConfigMap and republishes its content as Prometheus metrics
// on the agent's long-lived /metrics endpoint. The cluster-validator
// itself runs as a short-lived init container + CronJob, so it can't
// serve metrics directly — this reconciler bridges the gap.
type ValidatorSummaryReconciler struct {
	client             kubernetes.Interface
	namespace          string
	configMapName      string
	resetConfigMapName string
	metrics            *metrics.Metrics
}

// NewValidatorSummaryReconciler constructs the reconciler. The watcher
// is not started until Start() is called.
func NewValidatorSummaryReconciler(
	client kubernetes.Interface,
	namespace string,
	m *metrics.Metrics,
) *ValidatorSummaryReconciler {
	return &ValidatorSummaryReconciler{
		client:             client,
		namespace:          namespace,
		configMapName:      clustervalidator.SummaryConfigMapName,
		resetConfigMapName: clustervalidator.SummaryResetConfigMapName,
		metrics:            m,
	}
}

// cacheSyncTimeout bounds how long Start() waits for the informer's
// initial List before returning. A misconfigured watch namespace (or
// missing RBAC) makes the informer retry the List forever, so a blind
// WaitForCacheSync would hang agent startup. We bound the wait and, on
// timeout, return nil (non-fatal) — the informer keeps retrying in the
// background, so if RBAC/namespace is corrected later it will sync and
// publish without an agent restart.
const cacheSyncTimeout = 30 * time.Second

// Start launches the SharedInformer that watches the summary ConfigMap
// and publishes its content as metrics. The informer's initial List
// doubles as a bootstrap — if the ConfigMap already exists at startup
// (e.g. the agent restarted after a successful validator run), an Add
// event fires immediately and metrics are populated.
//
// Start blocks only briefly (bounded by cacheSyncTimeout) for the
// initial cache sync so the agent's /metrics endpoint serves consistent
// data soon after boot, but never hangs agent startup on a misconfigured
// watch — metrics are an SLI, not a gate.
func (r *ValidatorSummaryReconciler) Start(ctx context.Context) error {
	log := core.GetLogger(ctx).WithField("component", "cluster-validator-summary-reconciler").
		WithField("watch_namespace", r.namespace)

	// Preflight: a one-off GET surfaces an RBAC gap loudly before the
	// (silently-retrying) informer would otherwise just spin.
	r.ensureSummaryConfigMapAccess(ctx, log)

	// Filter the informer's List/Watch to just our one ConfigMap so we
	// don't pull every CM in the namespace into the agent's memory.
	fieldSelector := fmt.Sprintf("metadata.name=%s", r.configMapName)
	factory := informers.NewSharedInformerFactoryWithOptions(
		r.client,
		// Periodic full resync as a defensive belt-and-suspenders against
		// missed watch events. 5 min matches the cadence other agent
		// informers use.
		5*time.Minute,
		informers.WithNamespace(r.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fieldSelector
		}),
	)
	informer := factory.Core().V1().ConfigMaps().Informer()

	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == r.configMapName {
				r.onUpsert(log, cm)
			}
		},
		UpdateFunc: func(_, obj interface{}) {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == r.configMapName {
				r.onUpsert(log, cm)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Handles both a real ConfigMap and a DeletedFinalStateUnknown
			// tombstone (informer dropped events). Either way we preserve
			// last-known-good metrics — see onDelete.
			r.onDelete(log, obj)
		},
	}); err != nil {
		return fmt.Errorf("register cluster-validator summary informer handlers: %w", err)
	}

	// Second informer: a one-shot "reset" ConfigMap. Creating it is an
	// explicit operator action to clear the metrics; on observing it we
	// reset to baseline and delete it (consume the signal). A separate
	// factory is needed because each factory applies a single name
	// field-selector to every informer it builds.
	resetFactory := informers.NewSharedInformerFactoryWithOptions(
		r.client,
		5*time.Minute,
		informers.WithNamespace(r.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fmt.Sprintf("metadata.name=%s", r.resetConfigMapName)
		}),
	)
	resetInformer := resetFactory.Core().V1().ConfigMaps().Informer()
	if _, err := resetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == r.resetConfigMapName {
				r.onResetSignal(ctx, log, cm)
			}
		},
		// Deliberately NO UpdateFunc: a reset is a one-shot signal, and the
		// periodic resync re-delivers every cached object as a synthetic
		// Update. Handling Update here would re-fire the (destructive) reset
		// on every resync if the consume-delete had failed. AddFunc already
		// covers both "CM existed at startup" (initial List) and "CM created
		// at runtime"; a failed delete is retried on the next agent restart
		// via the initial List, not hammered every resync.
	}); err != nil {
		return fmt.Errorf("register cluster-validator reset informer handlers: %w", err)
	}

	factory.Start(ctx.Done())
	resetFactory.Start(ctx.Done())

	// Bounded wait for the initial List of BOTH informers. On timeout we
	// proceed anyway: the informers keep retrying in the background and will
	// populate metrics (and consume a leftover reset CM) once the watch
	// succeeds. Waiting on the reset informer too means a reset CM that
	// already exists at startup (e.g. a previous consume-delete failed) is
	// applied before Start returns, rather than racing the background List.
	syncCtx, cancel := context.WithTimeout(ctx, cacheSyncTimeout)
	defer cancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced, resetInformer.HasSynced) {
		log.Warnf("cluster-validator informers did not sync within %s; "+
			"continuing — metrics will populate once the watch on namespace %q succeeds",
			cacheSyncTimeout, r.namespace)
		return nil
	}

	log.Info("cluster-validator summary reconciler started")
	return nil
}

func (r *ValidatorSummaryReconciler) onUpsert(
	log *logrus.Entry, cm *corev1.ConfigMap,
) {
	log = log.WithField("cm", cm.Name)

	payload, ok := cm.Data[clustervalidator.SummaryConfigMapKey]
	if !ok {
		log.Warnf("cluster-validator summary CM missing data key %q; ignoring", clustervalidator.SummaryConfigMapKey)
		return
	}

	parsed, err := clustervalidator.ParseSummary([]byte(payload))
	if err != nil {
		// Don't reset metrics here — a transient JSON write or version-
		// drift situation should NOT surface as a false-bad SLI. Last
		// good data continues to be served. Operators detect drift via
		// the staleness alert on _last_run_timestamp_seconds.
		log.WithError(err).Warn("cluster-validator summary parse failed; preserving last-known-good metrics")
		return
	}

	ts, err := time.Parse(time.RFC3339, parsed.RanAt)
	if err != nil {
		log.WithError(err).Warnf("cluster-validator summary ranAt %q not RFC3339; preserving last-known-good metrics", parsed.RanAt)
		return
	}

	endpoints := make(map[string]metrics.ClusterValidatorEndpoint, len(parsed.Endpoints))
	for name, ep := range parsed.Endpoints {
		endpoints[name] = metrics.ClusterValidatorEndpoint{Reachable: ep.Reachable, Critical: ep.Critical}
	}
	pairs := make(map[string]metrics.ClusterValidatorNetpolPair, len(parsed.NetpolPairs))
	for name, p := range parsed.NetpolPairs {
		pairs[name] = metrics.ClusterValidatorNetpolPair{
			Passed:     p.Passed,
			Critical:   p.Critical,
			Directions: netpolDirectionsForMetrics(p),
		}
	}

	r.metrics.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{
		RanAtUnixSec:    ts.Unix(),
		DurationSeconds: parsed.DurationSeconds,
		VerdictReady:    parsed.VerdictReady,
		Checks:          parsed.Checks,
		Endpoints:       endpoints,
		NetpolPairs:     pairs,
	})

	log.WithField("verdict", parsed.Verdict).
		WithField("ranAt", parsed.RanAt).
		Debug("cluster-validator metrics updated")
}

// netpolDirectionsForMetrics maps the wire-format per-direction breakdown to
// the metrics package's mirror type. An empty Directions map is preserved as
// empty so no series are emitted for that pair: the validator omits a direction
// when it could not evaluate policies (e.g. a namespace in the pair is missing),
// and synthesizing zero-valued series here would misreport a policy block on
// every side. The pair's failure is still surfaced via the configurable_netpol
// check status and the validator's recommendation. Directions is part of
// summary schema v1, so there is no older format that would need synthesis.
func netpolDirectionsForMetrics(p clustervalidator.PairStatus) map[string]metrics.ClusterValidatorNetpolDirection {
	out := make(map[string]metrics.ClusterValidatorNetpolDirection, len(p.Directions))
	for direction, d := range p.Directions {
		out[direction] = metrics.ClusterValidatorNetpolDirection{
			EgressAllowed:  d.EgressAllowed,
			IngressAllowed: d.IngressAllowed,
		}
	}
	return out
}

func (r *ValidatorSummaryReconciler) onDelete(
	log *logrus.Entry, obj interface{},
) {
	// A deletion does NOT reset metrics: an accidental delete of the summary
	// ConfigMap (kubectl, a GC sweep, a reinstall) must not wipe the SLI.
	// Last-known-good values are preserved; genuine staleness is caught by
	// the last-run-timestamp alert. Resets happen only via the explicit
	// reset ConfigMap — see onResetSignal.
	switch v := obj.(type) {
	case *corev1.ConfigMap:
		log.WithField("cm", v.Name).Info("cluster-validator summary CM deleted; " +
			"preserving last-known-good metrics (create the reset ConfigMap to reset explicitly)")
	case cache.DeletedFinalStateUnknown:
		log.WithField("key", v.Key).Info("cluster-validator summary CM delete (tombstone); preserving last-known-good metrics")
	default:
		log.Warn("cluster-validator summary informer delete: unexpected object type; preserving last-known-good metrics")
	}
}

// onResetSignal handles the explicit reset ConfigMap. Creating that ConfigMap
// is a deliberate operator action to clear the cluster-validator metrics back
// to baseline. We reset, then delete the ConfigMap to consume the one-shot
// signal so it doesn't re-fire on the next informer resync or agent restart.
func (r *ValidatorSummaryReconciler) onResetSignal(ctx context.Context, log *logrus.Entry, cm *corev1.ConfigMap) {
	log.WithField("cm", cm.Name).Info("cluster-validator metrics reset requested; resetting to baseline")
	r.metrics.ResetClusterValidatorMetrics()

	delCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := r.client.CoreV1().ConfigMaps(r.namespace).Delete(delCtx, cm.Name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		log.WithError(err).Warnf("reset ConfigMap %s/%s consumed (metrics reset) but could not be deleted; "+
			"delete it manually to avoid a re-reset on the next agent restart", r.namespace, cm.Name)
		return
	}
	log.Info("cluster-validator metrics reset complete; reset ConfigMap consumed")
}

// ensureSummaryConfigMapAccess does a one-off GET on the summary ConfigMap
// at startup. If it returns Forbidden, we log loudly so the operator
// notices the RBAC gap before metrics silently fail. Other errors
// (NotFound, transient API errors) are normal and ignored.
func (r *ValidatorSummaryReconciler) ensureSummaryConfigMapAccess(ctx context.Context, log *logrus.Entry) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := r.client.CoreV1().ConfigMaps(r.namespace).Get(checkCtx, r.configMapName, metav1.GetOptions{})
	switch {
	case err == nil, apierrors.IsNotFound(err):
		// ok
	case apierrors.IsForbidden(err):
		log.WithError(err).Errorf(
			"cluster-validator summary metrics will be unavailable: the agent's "+
				"ServiceAccount lacks get/watch on configmaps %q in namespace %q",
			r.configMapName, r.namespace)
	default:
		log.WithError(err).Warn("cluster-validator summary preflight check failed; metrics may be unavailable")
	}
}
