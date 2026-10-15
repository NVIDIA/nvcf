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

// Tests for the CRD status writer's handling of the content-addressed
// hash (nvsnap#61). Without these, a regression in setCheckpointStatus
// would silently strip hash from the GPUCheckpoint .status field —
// which is exactly the bug that triggered the GCP-H100-a retry storm
// observed 2026-05-31.

package server

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testHash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"

// newCheckpointCRD builds an unstructured GPUCheckpoint with the given
// name + namespace and an empty status. Used as the starting state for
// the writer tests below.
func newCheckpointCRD(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvsnap.io/v1alpha1",
		"kind":       "GPUCheckpoint",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"status": map[string]interface{}{},
	}}
}

// readStatusField pulls a string field off the CRD after a status
// write — used to assert both "we wrote it" and "we didn't wipe it."
//
//nolint:unparam // namespace kept explicit for test readability and future multi-ns cases
func readStatusField(t *testing.T, s *Server, name, namespace, field string) string {
	t.Helper()
	got, err := s.dynClient.Resource(checkpointGVR).Namespace(namespace).Get(
		context.Background(), name, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	status, _ := got.Object["status"].(map[string]interface{})
	if status == nil {
		return ""
	}
	v, _ := status[field].(string)
	return v
}

func TestSetCheckpointStatus_PopulatesHashOnCompleted(t *testing.T) {
	s := newTestServer()
	const ns, name = "ns1", "ck1"
	obj := newCheckpointCRD(name, ns)
	if _, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).Create(
		context.Background(), obj, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	live, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).Get(
		context.Background(), name, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	s.setCheckpointStatus(context.Background(), live, "Completed", "node-1",
		"/var/lib/nvsnap/checkpoints/x", testHash, 12345, "ok")

	if got := readStatusField(t, s, name, ns, "checkpointHash"); got != testHash {
		t.Errorf("status.checkpointHash = %q, want %q", got, testHash)
	}
	if got := readStatusField(t, s, name, ns, "phase"); got != "Completed" {
		t.Errorf("phase = %q, want Completed", got)
	}
}

// COALESCE behavior on the CRD side: a Failed retry coming in with
// hash="" must not wipe a prior Completed hash. Mirrors the SQL-side
// COALESCE in db.UpdateCheckpointStatus — same invariant, two layers.
func TestSetCheckpointStatus_PreservesPriorHashOnEmptyUpdate(t *testing.T) {
	s := newTestServer()
	const ns, name = "ns1", "ck2"

	// Seed with status.checkpointHash already populated (simulates a
	// prior successful capture's writeback).
	obj := newCheckpointCRD(name, ns)
	obj.Object["status"] = map[string]interface{}{
		"checkpointHash": testHash,
		"phase":          "Completed",
	}
	if _, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).Create(
		context.Background(), obj, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	live, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).Get(
		context.Background(), name, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	// Failed retry, no hash supplied — should preserve the prior one.
	s.setCheckpointStatus(context.Background(), live, "Failed", "node-1",
		"", "", 0, "agent timeout")

	if got := readStatusField(t, s, name, ns, "checkpointHash"); got != testHash {
		t.Errorf("hash got wiped on empty-hash update; got %q, want preserved %q", got, testHash)
	}
	if got := readStatusField(t, s, name, ns, "phase"); got != "Failed" {
		t.Errorf("phase = %q, want Failed (the update did happen)", got)
	}
}
