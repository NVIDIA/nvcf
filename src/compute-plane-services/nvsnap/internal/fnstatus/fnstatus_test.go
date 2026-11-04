// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package fnstatus

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		tenant string
		want   map[string]string
	}{
		{"nvcf", map[string]string{"function-version-id": "v1", "function-id": "f1", "nca-id": "t1"}, "nca-id",
			map[string]string{LabelNamespace: "ns", LabelFunctionVersion: "v1", LabelFunction: "f1", LabelTenant: "t1"}},
		{"upper case", map[string]string{"FUNCTION_VERSION_ID": "v1", "FUNCTION_ID": "f1"}, "",
			map[string]string{LabelNamespace: "ns", LabelFunctionVersion: "v1", LabelFunction: "f1"}},
		{"lower case first", map[string]string{"function-version-id": "v1", "FUNCTION_VERSION_ID": "v2"}, "",
			map[string]string{LabelNamespace: "ns", LabelFunctionVersion: "v1"}},
		{"outside nvcf", map[string]string{"team": "x"}, "nca-id", map[string]string{LabelNamespace: "ns"}},
		{"invalid value left out", map[string]string{"function-version-id": "a b", "function-id": strings.Repeat("x", 64)}, "",
			map[string]string{LabelNamespace: "ns"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Identity("ns", tc.labels, tc.tenant); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Identity = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestObjectName(t *testing.T) {
	for _, tc := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{LabelFunctionVersion: "v1", LabelNamespace: "ns"}, "v1"},
		{map[string]string{LabelNamespace: "ns"}, "ns-ns"},
		{nil, ""},
	} {
		if got := ObjectName(tc.labels); got != tc.want {
			t.Errorf("ObjectName(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
}

func record(name string, labels, data map[string]string) corev1.ConfigMap {
	return corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Data: data}
}

var v1 = map[string]string{LabelFunctionVersion: "v1", LabelFunction: "f1", LabelTenant: "t1", LabelNamespace: "sr-1"}

func complete(size string, extra map[string]string) map[string]string {
	d := map[string]string{"state": StateComplete, "size": size, "verifiedAt": "2026-11-03T10:00:00Z"}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func TestProject(t *testing.T) {
	recs := []corev1.ConfigMap{
		record(GroupRecordPrefix+"a", v1, complete("2", map[string]string{
			"checkpoint.0": "c0", "checkpoint.1": "c1", "node.0": "n0", "node.1": "n1", "captures": "1",
			"restores": "2026-11-03T10:05:00Z=80.0 2026-11-03T11:00:00Z=42.5", "gpuFaults": "1",
		})),
		// A second configuration of v1 that fell back to a complete cachedir.
		record(GroupRecordPrefix+"b", v1, map[string]string{"state": StateFailed, "size": "1", "reason": "preflight: MPTCP socket"}),
		record(CachedirRecordPrefix+"b", nil, map[string]string{"rank.0": StateComplete, "node.0": "n2"}),
		// Skipped: an alias, a record not named yet, other ConfigMaps.
		record(GroupRecordPrefix+"c", v1, map[string]string{"state": StateAlias, "alias": "a"}),
		record(GroupRecordPrefix+"d", nil, complete("1", map[string]string{"checkpoint.0": "x"})),
		record("nvsnap-cachedir-env", nil, map[string]string{"x": "y"}),
		// A version outside NVCF.
		record(GroupRecordPrefix+"e", map[string]string{LabelNamespace: "team"}, map[string]string{"state": StateCapturing, "size": "1"}),
	}
	got := Project(recs)
	if len(got) != 2 {
		t.Fatalf("Project named %d versions, want 2: %v", len(got), got)
	}
	s := got["v1"]
	if s.FunctionID != "f1" || s.Tenant != "t1" || s.Namespace != "sr-1" || s.FunctionVersionID != "v1" {
		t.Errorf("identity = %+v", s)
	}
	if s.State != StateComplete || s.Method != MethodCRIU || s.Ranks != 2 || s.Reason != "" || s.LastRestoreSecs != 42.5 {
		t.Errorf("summary = %+v", s)
	}
	if len(s.Configurations) != 2 || s.Configurations[0].Key != "a" || s.Configurations[1].Key != "b" {
		t.Fatalf("configurations = %+v", s.Configurations)
	}
	a, b := s.Configurations[0], s.Configurations[1]
	if !reflect.DeepEqual(a.Nodes, []string{"n0", "n1"}) || a.Restores != 2 || a.LastRestoreAt != "2026-11-03T11:00:00Z" || a.GPUFaults != 1 {
		t.Errorf("configuration a = %+v", a)
	}
	if b.Method != MethodCachedir || b.Cachedir != "1/1" || b.Reason != "preflight: MPTCP socket" {
		t.Errorf("configuration b = %+v", b)
	}
	if e := got["ns-team"]; e.State != StateCapturing || e.Method != "" || e.Namespace != "team" {
		t.Errorf("outside NVCF = %+v", e)
	}
}

func TestProject_Summary(t *testing.T) {
	for _, tc := range []struct {
		name               string
		data               []map[string]string
		cachedir           map[string]string
		state, method, why string
	}{
		{"stalled capture starts cold", []map[string]string{complete("1", map[string]string{"checkpoint.0": "c", "stalls": "3"})}, nil,
			StateStalled, "", ""},
		{"verifying restores its own instance", []map[string]string{{"state": StateVerifying, "size": "1", "checkpoint.0": "c", "sourceNamespace": "ns"}}, nil,
			StateVerifying, MethodCRIU, ""},
		{"failure without a cachedir capture is cold", []map[string]string{{"state": StateRestoreFailed, "size": "1", "lastRestoreFailure": "boom"}}, nil,
			StateRestoreFailed, "", "boom"},
		{"partial cachedir capture is cold", []map[string]string{{"state": StateGPUShareFailed, "size": "2", "gpushareFailure": "exit 1"}},
			map[string]string{"rank.0": StateComplete, "rank.1": StateCapturing}, StateGPUShareFailed, "", "exit 1"},
		{"capturing beats failed", []map[string]string{{"state": StateFailed, "size": "1"}, {"state": StateCapturing, "size": "1"}}, nil,
			StateCapturing, "", ""},
		{"complete without every checkpoint is not restored", []map[string]string{complete("2", map[string]string{"checkpoint.0": "c"})}, nil,
			StateComplete, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recs []corev1.ConfigMap
			for i, d := range tc.data {
				key := string(rune('a' + i))
				recs = append(recs, record(GroupRecordPrefix+key, v1, d))
				if tc.cachedir != nil {
					recs = append(recs, record(CachedirRecordPrefix+key, nil, tc.cachedir))
				}
			}
			s := Project(recs)["v1"]
			if s.State != tc.state || s.Method != tc.method || s.Reason != tc.why {
				t.Errorf("summary = %q/%q/%q, want %q/%q/%q", s.State, s.Method, s.Reason, tc.state, tc.method, tc.why)
			}
		})
	}
}

func TestProject_ReasonBounded(t *testing.T) {
	s := Project([]corev1.ConfigMap{record(GroupRecordPrefix+"a", v1, map[string]string{"state": StateFailed, "reason": strings.Repeat("x", 500)})})["v1"]
	if len(s.Reason) != reasonMax {
		t.Errorf("reason is %d bytes, want %d", len(s.Reason), reasonMax)
	}
}
