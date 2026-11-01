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

package selfhosted

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"nvcf-cli/internal/selfhosted/progress"
)

// PhaseID identifies the failing phase for category/remediation routing.
// String values match the orchestrator's emitPhase Name argument so
// agents can correlate with PhaseStarted/PhaseCompleted by name.
type PhaseID string

const (
	PhasePreflight    PhaseID = "preflight"
	PhaseResolve      PhaseID = "resolve-stack"
	PhaseRender       PhaseID = "render-cp"
	PhaseApplyCP      PhaseID = "apply-cp"
	PhaseCheckCP      PhaseID = "check-cp"
	PhaseRegister     PhaseID = "register"
	PhaseApplyCompute PhaseID = "apply-compute-plane"
	PhaseFinalCheck   PhaseID = "final-health"
)

// Failure wraps a Go error with metadata learned at the failure site. Callers
// in cmd/self_hosted_up.go fill whichever fields they have access to
// (Subprocess + ExitCode + Stderr for helmfile/oras; HTTPStatus for HTTP
// calls; KubernetesReason from k8s API errors).
type Failure struct {
	Phase            PhaseID
	Err              error     // the unwrapped or wrapped Go error
	Subprocess       string    // "helmfile" | "oras" | "kubectl" | "" if not subprocess
	ExitCode         int       // subprocess exit code; 0 if not applicable
	Stderr           io.Reader // optional: subprocess stderr; tail of 16 KB captured if non-nil
	HTTPStatus       int       // 0 if not an HTTP failure
	KubernetesReason string    // optional: e.g. "AlreadyExists", "Forbidden"
}

// Categorize converts a Failure into a fully-populated progress.PhaseFailed.
//
// Categorization table (phase × trigger → category / retry):
//
//	Phase               Trigger                                   Category               Retry
//	────────────────    ────────────────────────────────────────  ─────────────────────  ─────────────────────
//	preflight           any                                       cluster_state          after_remediation
//	resolve-stack       network dial error                        network                immediate
//	resolve-stack       "no such host" / DNS                      dns                    after_remediation
//	resolve-stack       other                                     internal               after_remediation
//	render-cp           any                                       helm_render            after_remediation
//	apply-cp            ctx deadline exceeded                     helm_apply             backoff (60s)
//	apply-cp            "another operation (install/upgrade)..."  helm_pending_upgrade   backoff (60s)
//	apply-cp            other                                     helm_apply             after_remediation
//	check-cp            429 rate-limit                            auth                   backoff (5s)
//	check-cp            401 / "token revoked"                     token_expiry           after_remediation
//	check-cp            other                                     auth                   after_remediation
//	register            409 / "already"                           cluster_state          after_remediation
//	register            5xx                                       partial_sis_write      after_remediation
//	register            other                                     register               after_remediation
//	apply-compute-plane any                                       compute_plane          after_remediation
//	final-health        any                                       cluster_state          after_remediation
//
// Remediation strings always end in actionable shell commands or doc URLs.
// "internal" and "unknown" are reserved for genuinely unexpected paths.
func Categorize(f Failure) progress.PhaseFailed {
	pf := progress.PhaseFailed{
		Num:        phaseNumFor(f.Phase),
		Name:       string(f.Phase),
		ErrMessage: f.Err.Error(),
		Raw: progress.RawFailure{
			Subprocess:       f.Subprocess,
			ExitCode:         f.ExitCode,
			StderrTail:       readTail(f.Stderr, 16*1024),
			HTTPStatus:       f.HTTPStatus,
			KubernetesReason: f.KubernetesReason,
		},
	}

	switch f.Phase {
	case PhasePreflight:
		pf.ErrCategory = "cluster_state"
		pf.RetryClass = "after_remediation"
		pf.Remediation = []string{
			"Address the failed pre-flight checks listed above",
			"Re-run with --debug for verbose output",
		}

	case PhaseResolve:
		if isDNSErr(f.Err) {
			// Check DNS before generic network — a net.DNSError is also a net.Error.
			pf.ErrCategory = "dns"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Verify DNS resolution for the bundle host",
				"Check /etc/resolv.conf or your DNS configuration",
			}
		} else if isNetworkErr(f.Err) {
			pf.ErrCategory = "network"
			pf.RetryClass = "immediate"
			pf.Remediation = []string{
				"Check network connectivity to the bundle source",
				"Retry the command",
			}
		} else {
			pf.ErrCategory = "internal"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Re-run with --debug for the full error trace",
			}
		}

	case PhaseRender:
		pf.ErrCategory = "helm_render"
		pf.RetryClass = "after_remediation"
		pf.Remediation = []string{
			"Inspect the helmfile error above",
			"Re-run with --debug to see the rendered manifests",
		}

	case PhaseApplyCP:
		msg := f.Err.Error()
		if errors.Is(f.Err, context.DeadlineExceeded) {
			pf.ErrCategory = "helm_apply"
			pf.RetryClass = "backoff"
			pf.RetryAfterSec = 60
			pf.Remediation = []string{
				"kubectl get pods --all-namespaces | grep -v Running",
				"kubectl describe pod -n cassandra-system <not-ready-pod>",
				"Retry after 60s",
			}
		} else if strings.Contains(msg, "another operation (install/upgrade") {
			pf.ErrCategory = "helm_pending_upgrade"
			pf.RetryClass = "backoff"
			pf.RetryAfterSec = 60
			pf.Remediation = []string{
				"helm list --all-namespaces --pending",
				"helm rollback <release> --history-max=10 || helm uninstall <release>",
				"Re-run nvcf self-hosted up",
			}
		} else {
			pf.ErrCategory = "helm_apply"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"kubectl describe pod -n <namespace> <pod>",
				"kubectl logs -n <namespace> <pod>",
				"Re-run with --debug",
			}
		}

	case PhaseCheckCP:
		if f.HTTPStatus == 429 {
			pf.ErrCategory = "auth"
			pf.RetryClass = "backoff"
			pf.RetryAfterSec = retryAfterFromError(f.Err)
			pf.Remediation = []string{
				"API Keys service rate-limited; retry after the suggested interval",
			}
		} else if f.HTTPStatus == 401 || strings.Contains(f.Err.Error(), "token revoked") {
			pf.ErrCategory = "token_expiry"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Token expired or revoked; obtain a new one with --token=$NEW_JWT or --refresh-token",
			}
		} else {
			pf.ErrCategory = "auth"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Verify the API Keys service is reachable",
				"Re-run with --token=$JWT or --refresh-token",
			}
		}

	case PhaseRegister:
		msg := f.Err.Error()
		if f.HTTPStatus == 409 || strings.Contains(msg, "already") {
			pf.ErrCategory = "cluster_state"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Cluster already registered with conflicting state",
				"nvcf self-hosted down --cluster-name=<name>  (full reset)",
				"OR re-run with the existing cluster's name to upgrade in place",
			}
		} else if f.HTTPStatus >= 500 {
			pf.ErrCategory = "partial_sis_write"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"SIS returned a 5xx; the cluster row may be partially written",
				"Re-run nvcf self-hosted up — SIS register is idempotent on conflict",
			}
		} else {
			pf.ErrCategory = "register"
			pf.RetryClass = "after_remediation"
			pf.Remediation = []string{
				"Inspect the SIS register error above",
				"Verify --nca-id and --cluster-name are correct",
			}
		}

	case PhaseApplyCompute:
		pf.ErrCategory = "compute_plane"
		pf.RetryClass = "after_remediation"
		pf.Remediation = []string{
			"kubectl describe deployment -n nvca-system nvca-operator",
			"kubectl logs -n nvca-system deployment/nvca-operator",
			"Re-run with --debug",
		}

	case PhaseFinalCheck:
		pf.ErrCategory = "cluster_state"
		pf.RetryClass = "after_remediation"
		pf.Remediation = []string{
			"Final health check failed; re-run nvcf self-hosted check",
		}

	default:
		// TODO(post-M+8.G): cache_corruption arm — auth.Probe surfaces this
		// category but authGatePhase5 falls through to re-mint silently
		// instead of categorizing the probe failure into a phase_failed event.
		// Wire it to PhaseCheckCP when the auth-gate decides to fail-fast on
		// probe corruption (e.g. when --strict-fingerprint is added).
		// TODO(M+8.I): cassandra_migration_lock — surfaced via fault-injection
		// harness in M+8.10d (toxiproxy + cassandra schema_migrations row).
		// Both categories are listed in event.go's PhaseFailed.ErrCategory enum
		// but have no current emit path. Falling through to "unknown" is
		// conservative; future callers should construct Failure with the
		// already-categorized error and bypass Categorize, OR we add explicit
		// arms here when the trigger paths land.
		pf.ErrCategory = "unknown"
		pf.RetryClass = "unknown"
	}

	return pf
}

// phaseNumFor maps a PhaseID to its SRD/SDD §6.4 phase number.
func phaseNumFor(p PhaseID) int {
	switch p {
	case PhasePreflight:
		return 1
	case PhaseResolve:
		return 2
	case PhaseRender:
		return 3
	case PhaseApplyCP:
		return 4
	case PhaseCheckCP:
		return 5
	case PhaseRegister:
		return 6
	case PhaseApplyCompute:
		return 7
	case PhaseFinalCheck:
		return 8
	}
	return 0
}

// isNetworkErr returns true for errors rooted in the net package
// (net.Error, *net.OpError) that indicate a connectivity failure.
func isNetworkErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// isDNSErr returns true for errors that indicate a DNS resolution failure.
// net.DNSError is a net.Error, so always check it before isNetworkErr.
func isDNSErr(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return strings.Contains(err.Error(), "no such host")
}

// readTail reads up to n bytes from the tail of r. Returns "" when r is nil.
func readTail(r io.Reader, n int) string {
	if r == nil {
		return ""
	}
	body, _ := io.ReadAll(r)
	if len(body) <= n {
		return string(body)
	}
	return string(body[len(body)-n:])
}

// retryAfterFromError performs a best-effort parse of a "Retry-After: Ns"
// value embedded in the error message. Defaults to 5 when not parseable.
func retryAfterFromError(err error) int {
	if err == nil {
		return 5
	}
	msg := err.Error()
	// Look for patterns like "Retry-After: 30" or "retry-after: 30s".
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, "retry-after:")
	if idx == -1 {
		return 5
	}
	rest := strings.TrimSpace(msg[idx+len("retry-after:"):])
	// Consume digits only (ignore trailing "s" or other suffix).
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 5
	}
	val := 0
	for _, ch := range rest[:end] {
		val = val*10 + int(ch-'0')
	}
	if val <= 0 {
		return 5
	}
	return val
}
