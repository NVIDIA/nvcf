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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Handler is an http.Handler that decodes Kubernetes AdmissionReview
// requests, hands the embedded Pod to a Mutator, and encodes the
// resulting JSON Patch into an AdmissionReview response.
//
// Fail-open at every layer:
//   - Decode error             → 400 (the apiserver will retry; never deny)
//   - Mutator returns error    → admit unchanged (allowed=true, no patch);
//     the customer's pod must always be admittable.
//   - Patch encoding error     → admit unchanged.
//   - No annotation / no match → admit unchanged (zero patches).
type Handler struct {
	Mutator *Mutator
	Log     logrus.FieldLogger
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()
	resp, err := h.Decide(r.Context(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger().WithError(err).Error("encode AdmissionReview response")
	}
}

// Decide is the testable seam: takes a raw AdmissionReview JSON body,
// returns the response AdmissionReview (always allowed=true). Errors
// are returned only for malformed input — every other failure is
// translated into "admit unchanged" inside the response.
func (h *Handler) Decide(ctx context.Context, body []byte) (*admissionv1.AdmissionReview, error) {
	if len(body) == 0 {
		return nil, errors.New("webhook: empty request body")
	}
	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		return nil, fmt.Errorf("decode AdmissionReview: %w", err)
	}
	if review.Request == nil {
		return nil, errors.New("webhook: AdmissionReview.Request is nil")
	}

	// Always echo group/version/kind + UID so the response is well-formed.
	out := &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &admissionv1.AdmissionResponse{
			UID:     review.Request.UID,
			Allowed: true,
		},
	}

	pod, err := decodePod(review.Request.Object.Raw)
	if err != nil {
		h.logger().WithError(err).Warn("decode pod from AdmissionRequest; admitting unchanged")
		return out, nil
	}

	patches, err := h.Mutator.Mutate(ctx, pod)
	if err != nil {
		h.logger().WithError(err).Warn("mutator error; admitting unchanged (fail-open)")
		return out, nil
	}
	if len(patches) == 0 {
		// No-op mutation; admit as-is.
		return out, nil
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		h.logger().WithError(err).Warn("encode patches; admitting unchanged")
		return out, nil
	}
	patchType := admissionv1.PatchTypeJSONPatch
	out.Response.PatchType = &patchType
	out.Response.Patch = patchBytes
	return out, nil
}

// decodePod accepts raw JSON for a corev1.Pod (the encoding the API
// server uses for AdmissionRequest.Object.Raw on Pod create). Returns
// nil pod + error if the bytes can't be decoded.
func decodePod(raw []byte) (*corev1.Pod, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty Object.Raw")
	}
	var pod corev1.Pod
	if err := json.Unmarshal(raw, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

func (h *Handler) logger() logrus.FieldLogger {
	if h.Log != nil {
		return h.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "webhook.admission")
}
