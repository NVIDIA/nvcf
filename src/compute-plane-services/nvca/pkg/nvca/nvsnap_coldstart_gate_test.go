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

package nvca

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// cfsWithLivePioneer builds a CFS already claimed by a different replica, so
// TryClaimColdStartPioneer returns (false, nil) — the lost-election path.
func cfsWithLivePioneer(fvID, pioneer string, expiry time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapFunctionStateGVR.Group + "/" + nvsnapFunctionStateGVR.Version,
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"status": map[string]any{
			"localCacheState":        "Cold",
			"coldStartPioneer":       pioneer,
			"coldStartPioneerExpiry": expiry.UTC().Format(time.RFC3339),
		},
	}}
}

// The gate reads the CFS, then claims. Both steps can disagree about whether
// the object exists: if the initial Get 404s and the winning replica creates
// and claims the CFS before our claim runs, the claim returns
// (claimed=false, err=nil) while our cfsObj is still nil.
//
// The lost-election branch then read cfsObj.Object for the pioneer name and
// panicked on the nil pointer, inside the MiniService creation path.
//
// Reproduced by 404-ing only the FIRST Get; the object exists for every call
// after that, which is exactly the interleaving above.
func TestShouldDeferColdStart_CFSAbsentAtLookupThenClaimLost(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	const fvID = "fv-race"
	c := newHookTestBackend(t, cfsWithLivePioneer(fvID, "other-ns/other-pod", time.Now().Add(30*time.Minute)))

	// Fail only the first Get, so cfsObj is nil while the claim still sees
	// the live foreign pioneer and returns claimed=false.
	fake := c.dynClient.(*fakedynamic.FakeDynamicClient)
	var gets int
	fake.PrependReactor("get", nvsnapFunctionStateGVR.Resource,
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			gets++
			if gets == 1 {
				return true, nil, apierrors.NewNotFound(
					schema.GroupResource{
						Group:    nvsnapFunctionStateGVR.Group,
						Resource: nvsnapFunctionStateGVR.Resource,
					}, fvID)
			}
			return false, nil, nil // fall through to the tracker
		})

	// Must not panic. Losing the election is expected here, so a deferral
	// error is the correct outcome; the assertion that matters is that we
	// got here at all rather than dying on a nil dereference.
	err := c.shouldDeferColdStart(context.Background(), newReq(fvID))
	if err == nil {
		t.Fatal("expected a deferral error after losing the election, got nil")
	}
	if gets < 2 {
		t.Errorf("expected the lost-election path to re-read the CFS; saw %d Get calls", gets)
	}
}

// Sanity: a CFS that is absent and stays absent means nobody else is claiming,
// so this request wins the election and proceeds (no deferral).
func TestShouldDeferColdStart_AbsentCFSWinsElection(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	c := newHookTestBackend(t) // no seed: CFS genuinely does not exist
	if err := c.shouldDeferColdStart(context.Background(), newReq("fv-solo")); err != nil {
		t.Errorf("solo replica should win its own election and proceed; got %v", err)
	}
}
