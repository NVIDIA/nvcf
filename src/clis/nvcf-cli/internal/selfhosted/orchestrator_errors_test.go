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
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCategorize(t *testing.T) {
	cases := []struct {
		name                string
		in                  Failure
		wantCategory        string
		wantRetryClass      string
		wantRetryAfter      int // 0 means "don't check"
		remediationIncludes string
		wantPhaseNum        int
	}{
		// ── apply-cp ─────────────────────────────────────────────────────────
		{
			name: "apply-cp ctx deadline → helm_apply backoff",
			in: Failure{
				Phase:      PhaseApplyCP,
				Err:        context.DeadlineExceeded,
				Subprocess: "helmfile",
				ExitCode:   1,
			},
			wantCategory:        "helm_apply",
			wantRetryClass:      "backoff",
			wantRetryAfter:      60,
			remediationIncludes: "kubectl describe",
			wantPhaseNum:        4,
		},
		{
			name: "apply-cp pending upgrade → helm_pending_upgrade backoff",
			in: Failure{
				Phase: PhaseApplyCP,
				Err:   errors.New("another operation (install/upgrade) is in progress"),
			},
			wantCategory:        "helm_pending_upgrade",
			wantRetryClass:      "backoff",
			wantRetryAfter:      60,
			remediationIncludes: "helm list",
			wantPhaseNum:        4,
		},
		{
			name: "apply-cp generic error → helm_apply after_remediation",
			in: Failure{
				Phase:      PhaseApplyCP,
				Err:        errors.New("some chart rendering failure"),
				Subprocess: "helmfile",
				ExitCode:   1,
			},
			wantCategory:        "helm_apply",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "kubectl describe",
			wantPhaseNum:        4,
		},
		// ── check-cp ─────────────────────────────────────────────────────────
		{
			name: "check-cp 429 → auth backoff",
			in: Failure{
				Phase:      PhaseCheckCP,
				Err:        errors.New("rate limit exceeded"),
				HTTPStatus: 429,
			},
			wantCategory:   "auth",
			wantRetryClass: "backoff",
			wantPhaseNum:   5,
		},
		{
			name: "check-cp 429 with Retry-After header → auth backoff with parsed delay",
			in: Failure{
				Phase:      PhaseCheckCP,
				Err:        errors.New("rate limit: Retry-After: 30"),
				HTTPStatus: 429,
			},
			wantCategory:   "auth",
			wantRetryClass: "backoff",
			wantRetryAfter: 30,
			wantPhaseNum:   5,
		},
		{
			name: "check-cp 401 → token_expiry after_remediation",
			in: Failure{
				Phase:      PhaseCheckCP,
				Err:        errors.New("unauthorized"),
				HTTPStatus: 401,
			},
			wantCategory:        "token_expiry",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "Token expired",
			wantPhaseNum:        5,
		},
		{
			name: "check-cp token revoked message → token_expiry after_remediation",
			in: Failure{
				Phase: PhaseCheckCP,
				Err:   errors.New("token revoked by admin"),
			},
			wantCategory:        "token_expiry",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "Token expired",
			wantPhaseNum:        5,
		},
		{
			name: "check-cp generic → auth after_remediation",
			in: Failure{
				Phase: PhaseCheckCP,
				Err:   errors.New("connection refused"),
			},
			wantCategory:   "auth",
			wantRetryClass: "after_remediation",
			wantPhaseNum:   5,
		},
		// ── register ─────────────────────────────────────────────────────────
		{
			name: "register 409 → cluster_state",
			in: Failure{
				Phase:      PhaseRegister,
				Err:        errors.New("cluster already exists"),
				HTTPStatus: 409,
			},
			wantCategory:        "cluster_state",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "nvcf self-hosted down",
			wantPhaseNum:        6,
		},
		{
			name: "register 'already' in message → cluster_state",
			in: Failure{
				Phase: PhaseRegister,
				Err:   errors.New("failed to register cluster: cluster already registered"),
			},
			wantCategory:   "cluster_state",
			wantRetryClass: "after_remediation",
			wantPhaseNum:   6,
		},
		{
			name: "register 503 → partial_sis_write",
			in: Failure{
				Phase:      PhaseRegister,
				Err:        errors.New("503 service unavailable"),
				HTTPStatus: 503,
			},
			wantCategory:        "partial_sis_write",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "idempotent",
			wantPhaseNum:        6,
		},
		{
			name: "register 400 → register after_remediation",
			in: Failure{
				Phase:      PhaseRegister,
				Err:        errors.New("400 bad request"),
				HTTPStatus: 400,
			},
			wantCategory:        "register",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "--nca-id",
			wantPhaseNum:        6,
		},
		// ── resolve-stack ─────────────────────────────────────────────────────
		{
			name: "resolve-stack DNS → dns after_remediation",
			in: Failure{
				Phase: PhaseResolve,
				Err:   errors.New("dial tcp: lookup foo.bar: no such host"),
			},
			wantCategory:        "dns",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "DNS",
			wantPhaseNum:        2,
		},
		{
			name: "resolve-stack net.DNSError → dns after_remediation",
			in: Failure{
				Phase: PhaseResolve,
				Err:   &net.DNSError{Err: "no such host", Name: "foo.bar"},
			},
			wantCategory:   "dns",
			wantRetryClass: "after_remediation",
			wantPhaseNum:   2,
		},
		{
			name: "resolve-stack net.OpError → network immediate",
			in: Failure{
				Phase: PhaseResolve,
				Err:   &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			},
			wantCategory:        "network",
			wantRetryClass:      "immediate",
			remediationIncludes: "connectivity",
			wantPhaseNum:        2,
		},
		{
			name: "resolve-stack generic → internal after_remediation",
			in: Failure{
				Phase: PhaseResolve,
				Err:   errors.New("unexpected nil pointer"),
			},
			wantCategory:   "internal",
			wantRetryClass: "after_remediation",
			wantPhaseNum:   2,
		},
		// ── preflight ─────────────────────────────────────────────────────────
		{
			name: "preflight → cluster_state after_remediation",
			in: Failure{
				Phase: PhasePreflight,
				Err:   errors.New("kubectl missing"),
			},
			wantCategory:        "cluster_state",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "pre-flight",
			wantPhaseNum:        1,
		},
		// ── render-cp ─────────────────────────────────────────────────────────
		{
			name: "render-cp → helm_render after_remediation",
			in: Failure{
				Phase: PhaseRender,
				Err:   errors.New("template rendering failed"),
			},
			wantCategory:        "helm_render",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "helmfile",
			wantPhaseNum:        3,
		},
		// ── apply-compute-plane ───────────────────────────────────────────────
		{
			name: "compute-plane → compute_plane after_remediation",
			in: Failure{
				Phase: PhaseApplyCompute,
				Err:   errors.New("nvca-operator failed"),
			},
			wantCategory:        "compute_plane",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "nvca-system",
			wantPhaseNum:        7,
		},
		// ── final-health ──────────────────────────────────────────────────────
		{
			name: "final-health → cluster_state after_remediation",
			in: Failure{
				Phase: PhaseFinalCheck,
				Err:   errors.New("backend unreachable"),
			},
			wantCategory:        "cluster_state",
			wantRetryClass:      "after_remediation",
			remediationIncludes: "self-hosted check",
			wantPhaseNum:        8,
		},
		// ── unknown phase ─────────────────────────────────────────────────────
		{
			name: "unknown phase → unknown/unknown",
			in: Failure{
				Phase: PhaseID("bogus-phase"),
				Err:   errors.New("wat"),
			},
			wantCategory:   "unknown",
			wantRetryClass: "unknown",
			wantPhaseNum:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Categorize(tc.in)

			assert.Equal(t, tc.wantCategory, got.ErrCategory, "ErrCategory")
			assert.Equal(t, tc.wantRetryClass, got.RetryClass, "RetryClass")
			if tc.wantRetryAfter != 0 {
				assert.Equal(t, tc.wantRetryAfter, got.RetryAfterSec, "RetryAfterSec")
			}
			assert.Equal(t, tc.wantPhaseNum, got.Num, "Num (phase number)")
			assert.Equal(t, string(tc.in.Phase), got.Name, "Name")
			assert.Equal(t, tc.in.Err.Error(), got.ErrMessage, "ErrMessage")

			// All categorized paths (except unknown) MUST provide at least one
			// remediation string.
			if tc.wantCategory != "unknown" {
				require.NotEmpty(t, got.Remediation, "Remediation must be non-empty for category %q", tc.wantCategory)
			}

			if tc.remediationIncludes != "" {
				joined := strings.Join(got.Remediation, "\n")
				assert.Contains(t, joined, tc.remediationIncludes, "remediation text")
			}
		})
	}
}

// TestCategorize_RawPassthrough verifies that Failure's raw metadata fields
// (Subprocess, ExitCode, Stderr, HTTPStatus, KubernetesReason) are faithfully
// copied into the returned PhaseFailed.Raw struct.
func TestCategorize_RawPassthrough(t *testing.T) {
	pf := Categorize(Failure{
		Phase:            PhaseApplyCP,
		Err:              errors.New("boom"),
		Subprocess:       "helmfile",
		ExitCode:         137,
		Stderr:           strings.NewReader("Error: timed out waiting for pods\n"),
		HTTPStatus:       0,
		KubernetesReason: "",
	})

	assert.Equal(t, "helmfile", pf.Raw.Subprocess, "Raw.Subprocess")
	assert.Equal(t, 137, pf.Raw.ExitCode, "Raw.ExitCode")
	assert.Contains(t, pf.Raw.StderrTail, "timed out", "Raw.StderrTail should include stderr content")
	assert.Equal(t, 0, pf.Raw.HTTPStatus, "Raw.HTTPStatus")
}

// TestCategorize_RawHTTPStatus verifies that HTTPStatus passes through for
// HTTP-bearing failures.
func TestCategorize_RawHTTPStatus(t *testing.T) {
	pf := Categorize(Failure{
		Phase:      PhaseRegister,
		Err:        errors.New("503 service unavailable"),
		HTTPStatus: 503,
	})

	assert.Equal(t, 503, pf.Raw.HTTPStatus, "Raw.HTTPStatus must pass through")
}

// TestCategorize_KubernetesReasonPassthrough verifies that KubernetesReason
// passes through untouched.
func TestCategorize_KubernetesReasonPassthrough(t *testing.T) {
	pf := Categorize(Failure{
		Phase:            PhaseApplyCP,
		Err:              errors.New("Forbidden"),
		KubernetesReason: "Forbidden",
	})

	assert.Equal(t, "Forbidden", pf.Raw.KubernetesReason, "Raw.KubernetesReason must pass through")
}

// TestCategorize_StderrTailTruncates verifies that stderr content beyond 16 KB
// is truncated to exactly 16 KB (keeping the tail, not the head).
func TestCategorize_StderrTailTruncates(t *testing.T) {
	const tailContent = "TAIL-MARKER"
	// Build a >16 KB blob with a recognisable tail marker.
	head := strings.Repeat("X", 20*1024)
	full := head + tailContent
	pf := Categorize(Failure{
		Phase:  PhaseApplyCP,
		Err:    errors.New("boom"),
		Stderr: strings.NewReader(full),
	})

	assert.Equal(t, 16*1024, len(pf.Raw.StderrTail), "StderrTail must be exactly 16 KB when input exceeds limit")
	assert.True(t, strings.HasSuffix(pf.Raw.StderrTail, tailContent),
		"StderrTail must end with the tail of stderr, not the head")
}

// TestCategorize_StderrNil verifies that a nil Stderr reader produces an empty
// StderrTail without panicking.
func TestCategorize_StderrNil(t *testing.T) {
	pf := Categorize(Failure{
		Phase:  PhaseApplyCP,
		Err:    errors.New("boom"),
		Stderr: nil,
	})
	assert.Equal(t, "", pf.Raw.StderrTail, "nil Stderr must produce empty StderrTail")
}

// TestCategorize_HelmfileDeadlineExceeded is the canonical integration-style
// test called out in the spec: drives the apply-cp failure path with a
// realistic helmfile "context deadline exceeded" error and asserts the full
// structured PhaseFailed shape.
func TestCategorize_HelmfileDeadlineExceeded(t *testing.T) {
	stderrContent := "Error: context deadline exceeded\nhelmfile: timed out waiting for release cassandra\n"
	pf := Categorize(Failure{
		Phase:      PhaseApplyCP,
		Err:        context.DeadlineExceeded,
		Subprocess: "helmfile",
		ExitCode:   1,
		Stderr:     strings.NewReader(stderrContent),
	})

	assert.Equal(t, "helm_apply", pf.ErrCategory, "ErrCategory")
	assert.Equal(t, "backoff", pf.RetryClass, "RetryClass")
	assert.Equal(t, 60, pf.RetryAfterSec, "RetryAfterSec")

	joined := strings.Join(pf.Remediation, "\n")
	assert.Contains(t, joined, "kubectl describe", "remediation must reference kubectl describe")

	assert.Equal(t, "helmfile", pf.Raw.Subprocess, "Raw.Subprocess")
	assert.NotEqual(t, 0, pf.Raw.ExitCode, "Raw.ExitCode must be non-zero")
	assert.NotEmpty(t, pf.Raw.StderrTail, "Raw.StderrTail must be non-empty")
	assert.Contains(t, pf.Raw.StderrTail, "deadline exceeded", "StderrTail must contain stderr text")
}

// TestRetryAfterFromError_Parsing verifies the Retry-After value extractor.
func TestRetryAfterFromError_Parsing(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{"rate limit: Retry-After: 30", 30},
		{"Retry-After: 120s", 120},
		{"retry-after: 5", 5},
		{"no header here", 5},    // default
		{"Retry-After: ", 5},     // no digits → default
		{"Retry-After: 0", 5},    // zero → default
		{"Retry-After: 300 ok", 300}, // stops at first non-digit
	}
	for _, tc := range cases {
		got := retryAfterFromError(errors.New(tc.msg))
		assert.Equal(t, tc.want, got, "retryAfterFromError(%q)", tc.msg)
	}
}

// TestPhaseNumFor verifies the phase number mapping covers all known phases.
func TestPhaseNumFor(t *testing.T) {
	cases := []struct {
		phase PhaseID
		want  int
	}{
		{PhasePreflight, 1},
		{PhaseResolve, 2},
		{PhaseRender, 3},
		{PhaseApplyCP, 4},
		{PhaseCheckCP, 5},
		{PhaseRegister, 6},
		{PhaseApplyCompute, 7},
		{PhaseFinalCheck, 8},
		{PhaseID("unknown-phase"), 0},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, phaseNumFor(tc.phase), "phaseNumFor(%q)", tc.phase)
	}
}
