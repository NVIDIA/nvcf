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
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	echo "github.com/labstack/echo/v4"
	"golang.org/x/sync/singleflight"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	modelOwner       = "inference-endpoints"
	modelsPathPrefix = "/v1/models/"
)

var errModelListingUnsupported = errors.New("provider does not list models")

// openAIModel and openAIModelList are the strict OpenAI model objects.
// models.OAIModel carries extra non-OpenAI fields, so it is not used here.
type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

// modelCatalog caches the router's model listing for ttl and remembers when
// this process first saw each model. It keeps nothing past ttl: once the
// cached listing expires, a failed refresh fails the caller.
type modelCatalog struct {
	lister provider.ModelLister
	ttl    time.Duration
	now    func() time.Time

	// refreshes lets concurrent callers share one router call.
	refreshes singleflight.Group

	mu        sync.Mutex
	listing   *provider.ModelListing
	expiresAt time.Time
	firstSeen map[string]int64
}

func newModelCatalog(lister provider.ModelLister, ttl time.Duration) *modelCatalog {
	return &modelCatalog{
		lister:    lister,
		ttl:       ttl,
		now:       time.Now,
		firstSeen: map[string]int64{},
	}
}

// models returns the routable models sorted by id.
func (c *modelCatalog) models(ctx context.Context) ([]openAIModel, error) {
	listing, err := c.current(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]openAIModel, 0, len(listing.ModelIDs))
	for _, id := range listing.ModelIDs {
		result = append(result, openAIModel{
			ID:      id,
			Object:  models.ObjectModel,
			Created: c.firstSeen[id],
			OwnedBy: modelOwner,
		})
	}
	slices.SortFunc(result, func(a, b openAIModel) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

// current returns the cached listing, refreshing it once it has expired. A
// caller stops waiting when its own context ends; the shared refresh does not
// depend on any one caller and is bounded by the provider's timeout.
func (c *modelCatalog) current(ctx context.Context) (*provider.ModelListing, error) {
	c.mu.Lock()
	if c.listing != nil && c.now().Before(c.expiresAt) {
		listing := c.listing
		c.mu.Unlock()
		return listing, nil
	}
	c.mu.Unlock()

	refreshCtx := context.WithoutCancel(ctx)
	result := c.refreshes.DoChan("", func() (any, error) { return c.refresh(refreshCtx) })
	select {
	case refreshed := <-result:
		if refreshed.Err != nil {
			return nil, refreshed.Err
		}
		return refreshed.Val.(*provider.ModelListing), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *modelCatalog) refresh(ctx context.Context) (*provider.ModelListing, error) {
	if c.lister == nil {
		return nil, errModelListingUnsupported
	}
	listing, err := c.lister.ListModels(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.listing = nil
		return nil, err
	}
	now := c.now()
	for _, id := range listing.ModelIDs {
		if _, ok := c.firstSeen[id]; !ok {
			c.firstSeen[id] = now.Unix()
		}
	}
	c.listing = listing
	c.expiresAt = now.Add(c.ttl)
	return listing, nil
}

func (h *Handlers) ListModels(c echo.Context) error {
	data, err := h.modelCatalog.models(c.Request().Context())
	if err != nil {
		return modelListingUnavailable(err)
	}
	return c.JSON(http.StatusOK, openAIModelList{Object: models.ObjectList, Data: data})
}

// RetrieveModel serves GET /v1/models/{id}. Model ids contain slashes, so the
// id is the rest of the decoded path rather than the raw route parameter.
func (h *Handlers) RetrieveModel(c echo.Context) error {
	id := strings.TrimPrefix(c.Request().URL.Path, modelsPathPrefix)

	data, err := h.modelCatalog.models(c.Request().Context())
	if err != nil {
		return modelListingUnavailable(err)
	}
	for _, model := range data {
		if model.ID == id {
			return c.JSON(http.StatusOK, model)
		}
	}
	return modelNotFound(id)
}

func modelNotFound(id string) error {
	return echo.NewHTTPError(http.StatusNotFound, models.ErrorResponse{Error: models.Error{
		Code:    "model_not_found",
		Message: fmt.Sprintf("The model '%s' does not exist", id),
		Param:   "model",
		Type:    "invalid_request_error",
	}})
}

func modelListingUnavailable(cause error) error {
	return echo.NewHTTPError(http.StatusBadGateway, models.ErrorResponse{Error: models.Error{
		Code:    "model_listing_unavailable",
		Message: "The model listing is temporarily unavailable",
		Type:    "server_error",
	}}).SetInternal(cause)
}
