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

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	registryHealthy         = "Healthy"
	registryUnhealthy       = "Unhealthy"
	registryModelQueryParam = "model"
)

type registryResponse struct {
	GeneratedAt string          `json:"generatedAt"`
	Models      []registryModel `json:"models"`
}

type registryModel struct {
	Model    string            `json:"model"`
	Health   string            `json:"health"`
	Clusters []registryCluster `json:"clusters"`
}

type registryCluster struct {
	ClusterID         string `json:"clusterId"`
	RegisteredServers uint32 `json:"registeredServers"`
	HealthyServers    uint32 `json:"healthyServers"`
}

// Registry serves GET /v1/registry from the same cached router listing as
// GET /v1/models. An unlisted model filter is an empty list, not a 404.
func (h *Handlers) Registry(c echo.Context) error {
	filters, hasFilter := c.QueryParams()[registryModelQueryParam]
	if hasFilter && filters[0] == "" {
		return emptyModelFilter()
	}
	var filter string
	if hasFilter {
		filter = filters[0]
	}

	response, err := h.modelCatalog.registry(c.Request().Context(), filter)
	if err != nil {
		return modelListingUnavailable(err)
	}
	return c.JSON(http.StatusOK, response)
}

// registry returns the registered models sorted by model, limited to filter
// unless it is empty.
func (c *modelCatalog) registry(ctx context.Context, filter string) (registryResponse, error) {
	listing, err := c.current(ctx)
	if err != nil {
		return registryResponse{}, err
	}

	result := make([]registryModel, 0, len(listing.Models))
	for _, model := range listing.Models {
		if filter != "" && model.ModelID != filter {
			continue
		}
		result = append(result, newRegistryModel(model))
	}
	slices.SortFunc(result, func(a, b registryModel) int { return strings.Compare(a.Model, b.Model) })

	return registryResponse{
		GeneratedAt: listing.refreshedAt.UTC().Format(time.RFC3339),
		Models:      result,
	}, nil
}

func newRegistryModel(model provider.RegisteredModel) registryModel {
	result := registryModel{
		Model:    model.ModelID,
		Health:   registryUnhealthy,
		Clusters: make([]registryCluster, 0, len(model.Clusters)),
	}
	for _, cluster := range model.Clusters {
		if cluster.HealthyServers > 0 {
			result.Health = registryHealthy
		}
		result.Clusters = append(result.Clusters, registryCluster{
			ClusterID:         cluster.ClusterID,
			RegisteredServers: cluster.RegisteredServers,
			HealthyServers:    cluster.HealthyServers,
		})
	}
	slices.SortFunc(result.Clusters, func(a, b registryCluster) int { return strings.Compare(a.ClusterID, b.ClusterID) })
	return result
}

func emptyModelFilter() error {
	return echo.NewHTTPError(http.StatusBadRequest, models.ErrorResponse{Error: models.Error{
		Code:    "invalid_model_filter",
		Message: "The model filter must not be empty",
		Param:   registryModelQueryParam,
		Type:    "invalid_request_error",
	}})
}
