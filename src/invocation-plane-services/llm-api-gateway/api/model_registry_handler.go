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
	"errors"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	modelsEndpointPath   = "/v1/models"
	registryEndpointPath = "/v1/registry"

	objectList  = "list"
	objectModel = "model"
	modelOwner  = "nvidia"
)

// errRouterModelList is what a caller sees when the router's model list
// fails. The underlying error names the router's address, so it is logged
// rather than returned: the discovery endpoints may be public.
var errRouterModelList = errors.New("model list is unavailable from the router")

// ModelRegistryHandlers serves the read-only discovery endpoints built from
// the router's GET /v1/models.
type ModelRegistryHandlers struct {
	handlers *Handlers
}

// OpenAIModel is one entry of the OpenAI GET /v1/models list.
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// OpenAIModelList is the OpenAI GET /v1/models response.
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// RegistryModel aggregates the routable inference servers of one model.
type RegistryModel struct {
	Model string `json:"model"`
	// ClusterID is the cluster the servers registered from. The design is
	// single-cluster; if servers disagree it is their sorted distinct ids
	// joined with ",".
	ClusterID        string `json:"clusterId"`
	InferenceServers int    `json:"inferenceServers"`
}

// RegistryList is the GET /v1/registry response.
type RegistryList struct {
	Object string          `json:"object"`
	Data   []RegistryModel `json:"data"`
}

func (h *ModelRegistryHandlers) RegisterRoutes(group *echo.Group) {
	group.GET(modelsEndpointPath, h.ListModels)
	group.GET(registryEndpointPath, h.Registry)
}

// ListModels serves GET /v1/models in the OpenAI list shape, one entry per
// distinct model id in the router's order.
func (h *ModelRegistryHandlers) ListModels(c echo.Context) error {
	list, err := h.routerModels(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, OpenAIModelList{Object: objectList, Data: openAIModels(list)})
}

// Registry serves GET /v1/registry: per model, the cluster id and the number
// of routable inference servers, sorted by model.
func (h *ModelRegistryHandlers) Registry(c echo.Context) error {
	list, err := h.routerModels(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, RegistryList{
		Object: objectList,
		Data:   registryModels(c.Request().Context(), list),
	})
}

func (h *ModelRegistryHandlers) routerModels(c echo.Context) (*provider.RouterModelList, error) {
	if h.handlers.modelLister == nil {
		return nil, echo.NewHTTPError(http.StatusNotImplemented, "model listing is not configured")
	}

	ctx := c.Request().Context()
	list, err := h.handlers.modelLister.ListModels(ctx)
	if err != nil {
		telemetry.Logger(ctx).Warn().Err(err).Msg("router model list failed")
		if isTimeout(err) {
			return nil, echo.NewHTTPError(http.StatusGatewayTimeout, "model list request to the router timed out")
		}
		return nil, providerHTTPError(errRouterModelList)
	}
	if list == nil {
		list = &provider.RouterModelList{}
	}
	return list, nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// openAIModels returns one OpenAI model per distinct non-empty model id, in
// the router's order. ModelIDs comes first; entry ids are folded in so a list
// whose ModelIDs is incomplete still names every routable model.
func openAIModels(list *provider.RouterModelList) []OpenAIModel {
	data := make([]OpenAIModel, 0, len(list.ModelIDs))
	seen := make(map[string]struct{}, len(list.ModelIDs))
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		data = append(data, OpenAIModel{ID: id, Object: objectModel, OwnedBy: modelOwner})
	}
	for _, id := range list.ModelIDs {
		add(id)
	}
	for _, entry := range list.Entries {
		add(entry.ModelID)
	}
	return data
}

// registryModels aggregates the router's entries per model. Empty cluster ids
// are ignored.
func registryModels(ctx context.Context, list *provider.RouterModelList) []RegistryModel {
	type aggregate struct {
		clusters map[string]struct{}
		servers  int
	}
	byModel := make(map[string]*aggregate)
	for _, entry := range list.Entries {
		if entry.ModelID == "" {
			continue
		}
		agg, ok := byModel[entry.ModelID]
		if !ok {
			agg = &aggregate{clusters: map[string]struct{}{}}
			byModel[entry.ModelID] = agg
		}
		agg.servers++
		if entry.ClusterID != "" {
			agg.clusters[entry.ClusterID] = struct{}{}
		}
	}

	data := make([]RegistryModel, 0, len(byModel))
	for model, agg := range byModel {
		clusters := sortedKeys(agg.clusters)
		if len(clusters) > 1 {
			telemetry.Logger(ctx).
				Warn().
				Str("model", model).
				Strs("cluster_ids", clusters).
				Msg("router entries for one model disagree on cluster id")
		}
		data = append(data, RegistryModel{
			Model:            model,
			ClusterID:        strings.Join(clusters, ","),
			InferenceServers: agg.servers,
		})
	}
	sort.Slice(data, func(i, j int) bool {
		return data[i].Model < data[j].Model
	})
	return data
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
