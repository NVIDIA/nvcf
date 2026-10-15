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

package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// admissionRequest builds a minimal AdmissionReview JSON wrapping the given pod.
func admissionRequest(t *testing.T, pod *corev1.Pod) []byte {
	t.Helper()
	podBytes, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:    types.UID("admission-uid-1"),
			Kind:   metav1.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
			Object: runtime.RawExtension{Raw: podBytes},
		},
	}
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecide_NoAnnotationAdmitsUnchanged(t *testing.T) {
	b, _ := checkpointstore.NewLocal(t.TempDir())
	h := &Handler{Mutator: &Mutator{Backend: b}}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	resp, err := h.Decide(context.Background(), admissionRequest(t, pod))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Response == nil || !resp.Response.Allowed {
		t.Fatalf("expected allowed=true, got %+v", resp.Response)
	}
	if resp.Response.Patch != nil {
		t.Fatalf("no-annotation pod should have no patch; got %s", resp.Response.Patch)
	}
	if string(resp.Response.UID) != "admission-uid-1" {
		t.Errorf("UID echo broken: got %q", resp.Response.UID)
	}
}

func TestDecide_HappyPathProducesPatch(t *testing.T) {
	b, _ := checkpointstore.NewLocal(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	src := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: src}}, checkpointstore.Manifest{
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "v", MountPath: "/cache", Type: "emptyDir", FileCount: 1, SizeBytes: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Mutator: &Mutator{Backend: b}}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p", Namespace: "default",
			Annotations: map[string]string{RestoreFromAnnotation: hash},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	resp, err := h.Decide(context.Background(), admissionRequest(t, pod))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Response.Patch == nil {
		t.Fatalf("happy path expected patches; got nil")
	}
	if resp.Response.PatchType == nil || *resp.Response.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Errorf("PatchType should be JSONPatch; got %v", resp.Response.PatchType)
	}
	// Patch is JSON-encoded []PatchOp.
	var ops []map[string]any
	if err := json.Unmarshal(resp.Response.Patch, &ops); err != nil {
		t.Fatalf("patch is not valid JSON: %v\n%s", err, resp.Response.Patch)
	}
	if len(ops) == 0 {
		t.Fatalf("happy path should produce at least one op")
	}
}

func TestDecide_MalformedBodyErrors(t *testing.T) {
	h := &Handler{Mutator: &Mutator{Backend: nil}}
	_, err := h.Decide(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestDecide_EmptyBodyErrors(t *testing.T) {
	h := &Handler{Mutator: &Mutator{}}
	_, err := h.Decide(context.Background(), []byte{})
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestDecide_NilRequestErrors(t *testing.T) {
	h := &Handler{Mutator: &Mutator{}}
	bodyOnlyTypeMeta, _ := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
	})
	_, err := h.Decide(context.Background(), bodyOnlyTypeMeta)
	if err == nil {
		t.Fatal("expected error when AdmissionReview.Request is nil")
	}
}

func TestDecide_MalformedPodAdmitsUnchanged(t *testing.T) {
	h := &Handler{Mutator: &Mutator{}}
	// Object.Raw is valid JSON but not a Pod (a number, not an object).
	// json.Unmarshal into corev1.Pod fails with a type error.
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:    "x",
			Object: runtime.RawExtension{Raw: []byte(`42`)},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.Decide(context.Background(), body)
	if err != nil {
		t.Fatalf("malformed pod should NOT error (fail-open); got %v", err)
	}
	if !resp.Response.Allowed {
		t.Fatalf("malformed pod should still be admitted (fail-open)")
	}
}

func TestDecide_MutatorErrorAdmitsUnchanged(t *testing.T) {
	// Mutator returns an error for any non-empty annotation when Backend is nil.
	h := &Handler{Mutator: &Mutator{}} // Backend nil → Mutate returns error
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p", Namespace: "default",
			Annotations: map[string]string{RestoreFromAnnotation: "abc"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	resp, err := h.Decide(context.Background(), admissionRequest(t, pod))
	if err != nil {
		t.Fatalf("mutator error should be swallowed (fail-open); got %v", err)
	}
	if !resp.Response.Allowed {
		t.Fatalf("mutator error should not deny admission; got %+v", resp.Response)
	}
	if resp.Response.Patch != nil {
		t.Fatalf("mutator error should produce no patch; got %s", resp.Response.Patch)
	}
}

// TestServeHTTP_RoundTrip verifies the full HTTP path: request → response.
func TestServeHTTP_RoundTrip(t *testing.T) {
	b, _ := checkpointstore.NewLocal(t.TempDir())
	h := &Handler{Mutator: &Mutator{Backend: b}}
	srv := httptest.NewServer(h)
	defer srv.Close()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	body := admissionRequest(t, pod)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		t.Fatal(err)
	}
	if review.Response == nil || !review.Response.Allowed {
		t.Fatalf("expected allowed response; got %+v", review.Response)
	}
}

func TestServeHTTP_BadBodyReturns400(t *testing.T) {
	h := &Handler{Mutator: &Mutator{}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
