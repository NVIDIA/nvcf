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

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/render"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/httpapi"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval"
)

const timeout = 2 * time.Minute

var emptyList = json.RawMessage([]byte("[]"))

type HttpRevalService struct {
	Validate http.HandlerFunc
	Render   http.HandlerFunc
}

type RevalRunner interface {
	Run(ctx context.Context, cfg reval.Config, out io.Writer) (reval.Result, error)
}

// Wraps the reval.Handler into http handlers for Validation and Rendering.
func NewHttpService(h RevalRunner, meter metric.Meter) *HttpRevalService {
	requestsInvalidCount, err := meter.Int64Counter(
		"nvcf_reval_invalid_chart_total",
		metric.WithDescription("Number of helm charts marked invalid by the service"),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create nvcf_reval_invalid_chart_total metric: %v", err))
	}

	revalToHttpWrapper := func(includeOutput bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			request := &RevalRequest{}

			if err := render.Bind(r, request); err != nil {
				httpapi.ServeSimpleError(r, w, "Error parsing JSON", err, http.StatusBadRequest)
				return
			}

			revalConfig := makeReValConfig(request)
			revalConfig.Render = includeOutput

			var buf io.Writer
			if includeOutput {
				buf = &bytes.Buffer{}
			}

			res, err := h.Run(ctx, revalConfig, buf)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				render.JSON(w, r, struct {
					Error string `json:"error"`
				}{Error: err.Error()})
				return
			}

			if !res.Valid {
				var method string
				if includeOutput {
					method = "render"
				} else {
					method = "validate"
				}
				requestsInvalidCount.Add(ctx, 1, metric.WithAttributeSet(attribute.NewSet(
					attribute.String("method", method),
				)))
			}

			result := &RevalResult{
				Valid:            res.Valid,
				ValidationErrors: errsToStrs(res.ValidationErrors),
			}
			for _, vpr := range res.ValidationPolicies {
				result.ValidationPolicies = append(result.ValidationPolicies, RevalPolicyResult{
					ID:               vpr.ID,
					Valid:            vpr.Valid,
					ValidationErrors: errsToStrs(vpr.ValidationErrors),
				})
			}

			if includeOutput {
				if result.Valid {
					result.Output = buf.(*bytes.Buffer).Bytes()
				} else {
					// Output is a JSON list, so just return an empty list.
					result.Output = emptyList
				}
			}

			render.JSON(w, r, result)
		}
	}

	return &HttpRevalService{
		Validate: revalToHttpWrapper(false),
		Render:   revalToHttpWrapper(true),
	}
}

func errsToStrs(errs []error) (strs []string) {
	strs = make([]string, len(errs))
	for i, err := range errs {
		strs[i] = err.Error()
	}
	return strs
}

func makeReValConfig(req *RevalRequest) reval.Config {
	cfg := reval.Config{
		ChartURL:  req.HelmChart,
		Namespace: req.Namespace,
		// Override this for backwards-compatibility.
		ReleaseName:             "mini-service",
		Values:                  req.Configuration,
		K8sVersion:              req.K8SVersion,
		APIVersions:             req.ApiVersions,
		NGCAPIKey:               req.ApiKey,
		HelmRegistryAuthConfig:  req.HelmRegistryAuthConfig,
		ImageRegistryAuthConfig: req.ImageRegistryAuthConfig,
	}

	// Only set these fields if service name and port are non-empty,
	// since the handler will fail if service name is set but at least port is not.
	if req.HelmChartServiceName != "" && req.HelmChartServicePort != 0 {
		cfg.TargetServiceName = req.HelmChartServiceName
		cfg.TargetServicePort = req.HelmChartServicePort
		cfg.TargetHTTPHealthEndpoint = req.HelmChartHTTPHealthEndpoint
	}

	for _, vp := range req.ValidatePolicies {
		cvp := reval.ValidationPolicy{
			ID:   vp.ID,
			Name: vp.Name,
		}
		for _, t := range vp.AllowedExtraKubernetesTypes {
			cvp.ExtraGVKs = append(cvp.ExtraGVKs, schema.GroupVersionKind{
				Group:   t.Group,
				Version: t.Version,
				Kind:    t.Kind,
			})
		}
		cfg.ValidatePolicies = append(cfg.ValidatePolicies, cvp)
	}

	if req.RenderPolicy != nil {
		cfg.RenderPolicy = reval.ValidationPolicy{
			Name: req.RenderPolicy.Name,
		}
		for _, t := range req.RenderPolicy.AllowedExtraKubernetesTypes {
			cfg.RenderPolicy.ExtraGVKs = append(cfg.RenderPolicy.ExtraGVKs, schema.GroupVersionKind{
				Group:   t.Group,
				Version: t.Version,
				Kind:    t.Kind,
			})
		}
	}

	return cfg
}
