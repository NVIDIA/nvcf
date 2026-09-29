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

// Package prober computes the Ready condition of an InferenceEndpoint: the
// Service exists, it has a ready endpoint, the backend answers health.path
// with 2xx, and for the chat format the backend's model listing contains
// spec.modelName.
package prober

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
)

const (
	// DefaultTimeout bounds each HTTP request of a probe.
	DefaultTimeout = 5 * time.Second

	// ModelsPath is the model listing of the chat format.
	ModelsPath = "/v1/models"

	// maxListedModels caps the model names quoted in a mismatch message.
	maxListedModels = 5
	// maxModelsBodyBytes caps how much of a model listing is read.
	maxModelsBodyBytes = 1 << 20
	// maxDrainBytes caps how much of a health response body is drained so
	// the connection can be reused.
	maxDrainBytes = 64 << 10
)

// Result is the computed Ready condition.
type Result struct {
	Status  metav1.ConditionStatus
	Reason  pylonv1alpha1.ReadyReason
	Message string
}

// Prober probes backends over HTTP. Client, BaseURL and Timeout are
// injectable for tests.
type Prober struct {
	// Client sends the probe requests.
	Client *http.Client
	// BaseURL returns the scheme, host and port of the backend, without a
	// trailing slash.
	BaseURL func(*pylonv1alpha1.InferenceEndpoint) string
	// Timeout bounds each request.
	Timeout time.Duration
	// Metrics records probe durations and failures. Nil disables recording.
	Metrics *metrics.Metrics
}

// New returns a Prober that reaches backends through their Service DNS name.
func New(m *metrics.Metrics) *Prober {
	return &Prober{
		Client:  &http.Client{},
		BaseURL: ServiceBaseURL,
		Timeout: DefaultTimeout,
		Metrics: m,
	}
}

// ServiceBaseURL is http://<service>.<namespace>.svc:<port>.
func ServiceBaseURL(ep *pylonv1alpha1.InferenceEndpoint) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", ep.Spec.Service.Name, ep.Namespace, ep.Spec.Service.Port)
}

// Probe computes Ready for ep. svc is nil when the Service does not exist;
// slices are the Service's EndpointSlices.
func (p *Prober) Probe(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, svc *corev1.Service, slices []discoveryv1.EndpointSlice) Result {
	res := p.probe(ctx, ep, svc, slices)
	if res.Status != metav1.ConditionTrue {
		p.Metrics.IncProbeFailure(res.Reason)
	}
	return res
}

func (p *Prober) probe(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, svc *corev1.Service, slices []discoveryv1.EndpointSlice) Result {
	if svc == nil {
		return failed(pylonv1alpha1.ReadyReasonServiceNotFound,
			fmt.Sprintf("Service %q not found in namespace %q", ep.Spec.Service.Name, ep.Namespace))
	}
	port, ok := servicePort(svc, ep.Spec.Service.Port)
	if !ok {
		return failed(pylonv1alpha1.ReadyReasonHealthProbeFailed,
			fmt.Sprintf("Service %q does not expose port %d", svc.Name, ep.Spec.Service.Port))
	}
	if ReadyEndpoints(port, slices) == 0 {
		return failed(pylonv1alpha1.ReadyReasonNoReadyEndpoints,
			fmt.Sprintf("Service %q has no ready endpoints for port %d", svc.Name, ep.Spec.Service.Port))
	}

	base := strings.TrimSuffix(p.BaseURL(ep), "/")
	healthURL := base + ep.Spec.Health.Path
	start := time.Now()
	code, err := p.health(ctx, healthURL)
	p.Metrics.ObserveProbeDuration(time.Since(start))
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failed(pylonv1alpha1.ReadyReasonHealthProbeFailed,
			fmt.Sprintf("GET %s timed out after %s", healthURL, p.timeout()))
	case err != nil:
		return failed(pylonv1alpha1.ReadyReasonHealthProbeFailed,
			fmt.Sprintf("GET %s failed: %v", healthURL, err))
	case code < 200 || code > 299:
		return failed(pylonv1alpha1.ReadyReasonHealthProbeFailed,
			fmt.Sprintf("GET %s returned HTTP %d", healthURL, code))
	}

	if ep.Spec.InferenceAPIFormat.Type == pylonv1alpha1.InferenceAPIFormatChat {
		if res, mismatch := p.checkModelName(ctx, ep, base+ModelsPath); mismatch {
			return res
		}
	}
	return Result{
		Status:  metav1.ConditionTrue,
		Reason:  pylonv1alpha1.ReadyReasonHealthProbeSucceeded,
		Message: fmt.Sprintf("GET %s returned HTTP %d", healthURL, code),
	}
}

// checkModelName fetches the model listing. It reports a mismatch only when
// the listing parses as an OpenAI model list that lacks spec.modelName; an
// unavailable or unparseable listing never fails Ready.
func (p *Prober) checkModelName(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, url string) (Result, bool) {
	logger := log.FromContext(ctx).V(1).WithValues("url", url)
	models, err := p.models(ctx, url)
	if err != nil {
		logger.Info("Skipping the model name check", "reason", err.Error())
		return Result{}, false
	}
	for _, m := range models {
		if m == ep.Spec.ModelName {
			return Result{}, false
		}
	}
	return failed(pylonv1alpha1.ReadyReasonModelNameMismatch,
		fmt.Sprintf("backend does not serve model %q; GET %s lists %s", ep.Spec.ModelName, ModelsPath, describeModels(models))), true
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultTimeout
}

func (p *Prober) get(ctx context.Context, url string) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

func (p *Prober) health(ctx context.Context, url string) (int, error) {
	resp, cancel, err := p.get(ctx, url)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	return resp.StatusCode, nil
}

// modelList is the subset of the OpenAI model list the check reads.
type modelList struct {
	Data *[]struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *Prober) models(ctx context.Context, url string) ([]string, error) {
	resp, cancel, err := p.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("model listing returned HTTP %d", resp.StatusCode)
	}
	var list modelList
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelsBodyBytes)).Decode(&list); err != nil {
		return nil, fmt.Errorf("model listing is not JSON: %w", err)
	}
	if list.Data == nil {
		return nil, errors.New("model listing has no data array")
	}
	models := make([]string, 0, len(*list.Data))
	for _, m := range *list.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// ReadyEndpoints counts ready endpoints in slices that serve port. An
// endpoint with an unknown ready state counts as ready, following the
// EndpointSlice API guidance.
func ReadyEndpoints(port corev1.ServicePort, slices []discoveryv1.EndpointSlice) int {
	n := 0
	for i := range slices {
		if !slicePortMatches(slices[i], port.Name) {
			continue
		}
		for _, e := range slices[i].Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				n++
			}
		}
	}
	return n
}

func slicePortMatches(slice discoveryv1.EndpointSlice, name string) bool {
	if len(slice.Ports) == 0 {
		return true
	}
	for _, p := range slice.Ports {
		if p.Name == nil || *p.Name == name {
			return true
		}
	}
	return false
}

func servicePort(svc *corev1.Service, port int32) (corev1.ServicePort, bool) {
	for _, p := range svc.Spec.Ports {
		if p.Port == port {
			return p, true
		}
	}
	return corev1.ServicePort{}, false
}

func describeModels(models []string) string {
	if len(models) == 0 {
		return "no models"
	}
	quoted := make([]string, 0, maxListedModels)
	for i, m := range models {
		if i == maxListedModels {
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", m))
	}
	out := strings.Join(quoted, ", ")
	if extra := len(models) - maxListedModels; extra > 0 {
		out += fmt.Sprintf(" and %d more", extra)
	}
	return out
}

func failed(reason pylonv1alpha1.ReadyReason, message string) Result {
	return Result{Status: metav1.ConditionFalse, Reason: reason, Message: message}
}
