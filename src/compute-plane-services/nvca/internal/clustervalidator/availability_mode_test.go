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
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseHAMode(t *testing.T) {
	for raw, want := range map[string]struct {
		mode  HAMode
		valid bool
	}{
		"":             {"", true},
		"  ":           {"", true},
		"none":         {HAModeNone, true},
		" Preferred ":  {HAModePreferred, true},
		"ENFORCED":     {HAModeEnforced, true},
		"best-effort":  {"", false},
		"enforced,ha":  {"", false},
		"preferred\nx": {"", false},
	} {
		mode, valid := parseHAMode(raw)
		assert.Equal(t, want.mode, mode, "%q", raw)
		assert.Equal(t, want.valid, valid, "%q", raw)
	}
}

// runControlPlaneLogged runs the control-plane set with NVCF_HA_MODE set to
// raw and returns its transcript and published summary.
func runControlPlaneLogged(t *testing.T, role Role, raw string) (string, *ValidatorSummary) {
	t.Helper()
	t.Setenv(HAModeEnv, raw)
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	ctx := core.WithLogger(context.Background(), logrus.NewEntry(l))
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	_ = Run(ctx, client, nil, "", "", "nvca-system", true, role)
	return buf.String(), readSummary(t, client, "nvca-system")
}

// The HA mode line comes right after the role line, so a launcher that
// forwarded a mode can tell an image that reads it from one that predates it.
func TestRun_PrintsTheHAModeMarkerAfterTheRoleMarker(t *testing.T) {
	for raw, want := range map[string]string{
		"enforced":    HAModeMarker + "enforced",
		" Preferred ": HAModeMarker + "preferred",
		"none":        HAModeMarker + "none",
		"":            HAModeMarker + "not supplied",
		"best-effort": HAModeMarker + "not supplied",
	} {
		out, _ := runControlPlaneLogged(t, RoleControlPlane, raw)
		lines := strings.Split(out, "\n")
		i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, RoleMarker+"control-plane") })
		require.GreaterOrEqual(t, i, 0, "%q", raw)
		require.Less(t, i+1, len(lines), "%q", raw)
		assert.Contains(t, lines[i+1], want, "%q: the HA mode line follows the role line", raw)
		assert.Equal(t, 1, strings.Count(out, HAModeMarker), "%q: one marker line and no other text "+
			"carrying the marker, which a launcher reads as support", raw)
	}
}

// readSummary reads back the summary a run published to ns.
func readSummary(t *testing.T, client kubernetes.Interface, ns string) *ValidatorSummary {
	t.Helper()
	cm, err := client.CoreV1().ConfigMaps(ns).Get(context.Background(), SummaryConfigMapName, metav1.GetOptions{})
	require.NoError(t, err, "Run must publish the summary ConfigMap")
	var s ValidatorSummary
	require.NoError(t, json.Unmarshal([]byte(cm.Data[SummaryConfigMapKey]), &s))
	return &s
}

// The compute-plane set judges no HA guarantee, so it neither prints the mode
// nor warns that none was supplied.
func TestRun_ComputePlaneIgnoresTheHAMode(t *testing.T) {
	out, s := runControlPlaneLogged(t, RoleComputePlane, "enforced")
	assert.NotContains(t, out, HAModeMarker)
	out, s2 := runControlPlaneLogged(t, RoleComputePlane, "")
	assert.NotContains(t, out, HAModeMarker)
	for _, sum := range []*ValidatorSummary{s, s2} {
		assert.NotContains(t, strings.Join(sum.Warnings, "; "), "HA mode")
	}
}

// Without a mode the invariants that depend on it are not graded, and one
// warning says so. An invalid value is named and treated as not supplied.
func TestRun_HAModeNotSuppliedOrInvalidWarns(t *testing.T) {
	_, s := runControlPlaneLogged(t, RoleControlPlane, "")
	assert.Contains(t, s.Warnings, haModeNotSuppliedWarning)
	assert.Equal(t, "HA mode not supplied; the HA invariants were checked against the rendered placement only",
		haModeNotSuppliedWarning)

	_, s = runControlPlaneLogged(t, RoleControlPlane, "best-effort")
	joined := strings.Join(s.Warnings, "; ")
	assert.Contains(t, joined, `NVCF_HA_MODE="best-effort"`)
	assert.Contains(t, s.Warnings, haModeNotSuppliedWarning)

	for _, mode := range []string{"none", "preferred", "enforced"} {
		_, s = runControlPlaneLogged(t, RoleControlPlane, mode)
		assert.NotContains(t, strings.Join(s.Warnings, "; "), "HA mode not supplied", mode)
		assert.NotContains(t, strings.Join(s.Warnings, "; "), HAModeEnv, mode)
	}
}

// The mode is carried on the state every check reads.
func TestReadHAMode_SetsTheState(t *testing.T) {
	for raw, want := range map[string]HAMode{"enforced": HAModeEnforced, "": "", "x": "", "none": HAModeNone} {
		mode, _ := readHAMode(testLog(), raw)
		assert.Equal(t, want, mode, "%q", raw)
	}
}
