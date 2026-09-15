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

package clustervalidator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Wire-format types for the cluster-validator summary ConfigMap.
//
// The validator writes a JSON document with this shape to a well-known
// ConfigMap at the end of every run. The NVCA agent watches that
// ConfigMap and exposes the values as Prometheus metrics via its
// long-lived /metrics endpoint. The schema is versioned so the writer
// and reader can evolve independently.

const (
	// SummarySchemaVersion is the value the writer puts in
	// ValidatorSummary.SchemaVersion. Bump when making backwards-
	// incompatible changes; the reader rejects unknown versions and
	// preserves the last-known-good metric values instead of crashing.
	SummarySchemaVersion = "v1"

	// SummaryConfigMapName is the well-known name of the ConfigMap the
	// validator writes its summary to. The agent watches this name in
	// the namespace named by SummaryConfigMapNamespaceEnv (the
	// operator/validator namespace), defaulting to the standard install
	// namespace.
	SummaryConfigMapName = "cluster-validator-summary"

	// SummaryConfigMapKey is the data key inside the ConfigMap that
	// holds the JSON payload.
	SummaryConfigMapKey = "summary.json"

	// SummaryResetConfigMapName is the well-known name of a one-shot
	// "reset" ConfigMap. The agent watches for it in the same namespace as
	// the summary; when an operator creates it, the agent resets the
	// cluster-validator metrics to baseline and then deletes it (consumes
	// the signal). This makes a metrics reset an explicit, deliberate
	// action — deleting the summary ConfigMap itself no longer resets
	// anything (last-known-good is preserved instead).
	SummaryResetConfigMapName = "cluster-validator-metrics-reset"

	// SummaryConfigMapNamespaceEnv names the env var that tells the NVCA
	// agent which namespace the cluster-validator writes its summary
	// ConfigMap to. This is the operator/validator namespace, which in
	// the standard split topology differs from the agent's own namespace
	// (validator runs in the operator namespace, e.g. nvca-operator; the
	// agent runs in the system namespace, e.g. nvca-system). The operator
	// injects this with its own namespace when it constructs the agent
	// Deployment.
	SummaryConfigMapNamespaceEnv = "VALIDATOR_SUMMARY_NAMESPACE"
)

// ValidatorSummary is the structured wire format the validator writes to
// SummaryConfigMapName at the end of every run. The fixed fields
// (SchemaVersion, RanAt, Verdict, etc.) and the fixed-key Checks map
// give the agent a stable shape to read. The Endpoints and NetpolPairs
// maps carry user-configured entries whose key set varies per cluster
// and per ConfigMap edit — the agent emits those as Prometheus label
// values (variable cardinality, bounded to the current run).
type ValidatorSummary struct {
	SchemaVersion   string                    `json:"schemaVersion"`
	RanAt           string                    `json:"ranAt"`           // run start time, RFC 3339 UTC.
	DurationSeconds float64                   `json:"durationSeconds"` // wall-clock for the whole run.
	Verdict         string                    `json:"verdict"`         // human label, e.g. "NVCF-Ready".
	VerdictReady    bool                      `json:"verdictReady"`    // load-bearing for SLI: cluster passed critical checks.
	Checks          map[string]bool           `json:"checks"`          // fixed key set; see CheckKey* constants.
	Endpoints       map[string]EndpointStatus `json:"endpoints,omitempty"`
	NetpolPairs     map[string]PairStatus     `json:"netpolPairs,omitempty"`
	Warnings        []string                  `json:"warnings,omitempty"`
}

// EndpointStatus is the per-endpoint payload inside Summary.Endpoints.
type EndpointStatus struct {
	Reachable bool `json:"reachable"`
	Critical  bool `json:"critical"`
}

// PairStatus is the per-pair payload inside Summary.NetpolPairs.
//
// Passed is the aggregate (both directions fully covered). Directions
// carries the per-direction, per-policy-side breakdown so the agent can
// publish a directional metric pinpointing which side (the source's egress
// or the destination's ingress) is missing. Directions is omitted by
// writers that predate the directional metric; readers treat a missing
// map as "no breakdown available" and fall back to the aggregate.
type PairStatus struct {
	Passed     bool                       `json:"passed"`
	Critical   bool                       `json:"critical"`
	Directions map[string]DirectionStatus `json:"directions,omitempty"`
}

// DirectionStatus is the per-policy-side outcome for one direction of a
// pair. A direction is fully covered when both EgressAllowed (the source
// namespace's egress permits the traffic) and IngressAllowed (the
// destination namespace's ingress permits it) are true.
type DirectionStatus struct {
	EgressAllowed  bool `json:"egressAllowed"`
	IngressAllowed bool `json:"ingressAllowed"`
}

// Netpol direction and policy-side identifiers. These are stable wire
// values and double as Prometheus label values on the directional
// netpol_pair_passed metric.
const (
	NetpolDirectionAToB = "a_to_b"
	NetpolDirectionBToA = "b_to_a"

	NetpolPolicySideEgress  = "egress"
	NetpolPolicySideIngress = "ingress"
)

// CheckKey* are the stable identifiers for the built-in checks. The agent
// initializes one Prometheus gauge per key at zero so the series appears
// on the first scrape even before any validator has run. Adding a new
// check requires adding a constant here AND updating the agent's
// init-to-zero list.
const (
	CheckKeyControlPlane           = "control_plane"
	CheckKeyWorkerNodesAllReady    = "worker_nodes_all_ready"
	CheckKeyWebhooks               = "webhooks"
	CheckKeyNetworkPoliciesSupport = "network_policies_supported"
	CheckKeySMBCSI                 = "smb_csi"
	CheckKeyEndpointReachability   = "endpoint_reachability"
	CheckKeyGPUResources           = "gpu_resources"
	CheckKeyGPUOperator            = "gpu_operator"
	CheckKeyConfigurableNetpol     = "configurable_netpol"
	CheckKeyNetpolEnforcement      = "netpol_enforcement"
)

// AllCheckKeys is the canonical ordering used for documentation and
// metric initialization. Order is the same as the printSummary output.
var AllCheckKeys = []string{
	CheckKeyControlPlane,
	CheckKeyWorkerNodesAllReady,
	CheckKeyWebhooks,
	CheckKeyNetworkPoliciesSupport,
	CheckKeySMBCSI,
	CheckKeyEndpointReachability,
	CheckKeyGPUResources,
	CheckKeyGPUOperator,
	CheckKeyConfigurableNetpol,
	CheckKeyNetpolEnforcement,
}

// buildSummary projects a ValidationState into the wire format. Checks
// that were not run (their *bool is nil) are omitted from the Checks
// map so the agent can distinguish "not run" from "ran and failed".
func buildSummary(state *ValidationState, startedAt time.Time, verdictReady bool, verdict string) *ValidatorSummary {
	now := time.Now().UTC()
	s := &ValidatorSummary{
		SchemaVersion: SummarySchemaVersion,
		// RanAt is the run's start time so the staleness SLI
		// (time() - last_run_timestamp_seconds) measures age from when the
		// run began, matching the "of the latest run" semantics rather than
		// underreporting age by the run's duration.
		RanAt:           startedAt.UTC().Format(time.RFC3339),
		DurationSeconds: now.Sub(startedAt).Seconds(),
		Verdict:         verdict,
		VerdictReady:    verdictReady,
		Checks:          map[string]bool{},
		Warnings:        append([]string(nil), state.Warnings...),
	}

	s.Checks[CheckKeyControlPlane] = state.ControlPlaneHealthy
	s.Checks[CheckKeyWorkerNodesAllReady] = state.NodesAllReady
	s.Checks[CheckKeyWebhooks] = state.WebhooksSupported
	s.Checks[CheckKeyNetworkPoliciesSupport] = state.NetworkPoliciesSupported
	s.Checks[CheckKeySMBCSI] = state.SMBCSIDriverOK
	s.Checks[CheckKeyGPUResources] = state.GPUAvailable
	s.Checks[CheckKeyGPUOperator] = state.GPUOperatorInstalled

	if state.ReachabilityOK != nil {
		s.Checks[CheckKeyEndpointReachability] = *state.ReachabilityOK
	}
	if state.ConfigurableNetPolOK != nil {
		s.Checks[CheckKeyConfigurableNetpol] = *state.ConfigurableNetPolOK
	}
	if state.EnforcementOK != nil {
		s.Checks[CheckKeyNetpolEnforcement] = *state.EnforcementOK
	}

	if len(state.EndpointResults) > 0 {
		s.Endpoints = make(map[string]EndpointStatus, len(state.EndpointResults))
		for name, r := range state.EndpointResults {
			s.Endpoints[name] = EndpointStatus(r)
		}
	}
	if len(state.NetpolPairResults) > 0 {
		s.NetpolPairs = make(map[string]PairStatus, len(state.NetpolPairResults))
		for name, r := range state.NetpolPairResults {
			s.NetpolPairs[name] = PairStatus(r)
		}
	}

	return s
}

// writeSummaryConfigMap persists the summary as JSON to a well-known
// ConfigMap. Creates the ConfigMap if it doesn't exist; otherwise
// updates the data in place. Errors are logged but do NOT fail the
// validator run — the metrics layer is an SLI, not a gate; failing the
// validator over a metrics-write failure would surface as a critical
// check failure to operators and is much worse than missing one data
// point.
func writeSummaryConfigMap(
	ctx context.Context,
	log *logrus.Entry,
	client kubernetes.Interface,
	namespace string,
	summary *ValidatorSummary,
) {
	payload, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		log.WithError(err).Warn("cluster-validator: failed to marshal summary; metrics will be stale")
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	existing, err := client.CoreV1().ConfigMaps(namespace).Get(writeCtx, SummaryConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SummaryConfigMapName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/component":  "cluster-validator",
					"app.kubernetes.io/managed-by": "cluster-validator",
				},
			},
			Data: map[string]string{SummaryConfigMapKey: string(payload)},
		}
		_, cerr := client.CoreV1().ConfigMaps(namespace).Create(writeCtx, cm, metav1.CreateOptions{})
		if cerr == nil {
			return
		}
		if !apierrors.IsAlreadyExists(cerr) {
			log.WithError(cerr).Warn("cluster-validator: failed to create summary ConfigMap; metrics will be stale")
			return
		}
		// A concurrent run created the ConfigMap between our Get and Create
		// (e.g. an overlapping CronJob and init-container run). That is not a
		// failure — re-read and fall through to Update so this run's results
		// win, rather than logging a misleading "metrics will be stale".
		existing, err = client.CoreV1().ConfigMaps(namespace).Get(writeCtx, SummaryConfigMapName, metav1.GetOptions{})
		if err != nil {
			log.WithError(err).Warn("cluster-validator: failed to re-read summary ConfigMap after create conflict; metrics will be stale")
			return
		}
	} else if err != nil {
		log.WithError(err).Warn("cluster-validator: failed to read summary ConfigMap; metrics will be stale")
		return
	}

	updated := existing.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string]string{}
	}
	updated.Data[SummaryConfigMapKey] = string(payload)
	if _, uerr := client.CoreV1().ConfigMaps(namespace).Update(writeCtx, updated, metav1.UpdateOptions{}); uerr != nil {
		log.WithError(uerr).Warn("cluster-validator: failed to update summary ConfigMap; metrics will be stale")
	}
}

// ParseSummary unmarshals a summary JSON document and validates its
// schema version. Exported for use by the agent-side reconciler.
func ParseSummary(payload []byte) (*ValidatorSummary, error) {
	var s ValidatorSummary
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("unmarshal summary JSON: %w", err)
	}
	if s.SchemaVersion != SummarySchemaVersion {
		return nil, fmt.Errorf("unsupported summary schema version %q (this build supports %q)",
			s.SchemaVersion, SummarySchemaVersion)
	}
	return &s, nil
}
