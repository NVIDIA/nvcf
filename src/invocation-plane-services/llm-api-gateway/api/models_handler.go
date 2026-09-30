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
	"github.com/maypok86/otter/v2"

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

// catalogClock drives both the cache expiry and the created timestamps, so
// tests control them together.
type catalogClock interface {
	otter.Clock
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                        { return time.Now() }
func (systemClock) NowNano() int64                        { return time.Now().UnixNano() }
func (systemClock) Tick(d time.Duration) <-chan time.Time { return time.Tick(d) }

// listingKey is the only key in modelCatalog.listings: the router returns one
// listing for all callers.
type listingKey struct{}

// modelCatalog caches the router's model listing for ttl and remembers when
// this process first saw each model. It keeps nothing past ttl: once the
// cached listing expires, a failed refresh fails the caller.
type modelCatalog struct {
	lister provider.ModelLister
	clock  catalogClock
	// listings is nil when ttl is zero, so every call reaches the router.
	listings *otter.Cache[listingKey, *provider.ModelListing]

	mu        sync.Mutex
	firstSeen map[string]int64
}

func newModelCatalog(lister provider.ModelLister, ttl time.Duration, clock catalogClock) *modelCatalog {
	catalog := &modelCatalog{
		lister:    lister,
		clock:     clock,
		firstSeen: map[string]int64{},
	}
	if ttl > 0 {
		catalog.listings = otter.Must(&otter.Options[listingKey, *provider.ModelListing]{
			MaximumSize:      1,
			ExpiryCalculator: otter.ExpiryWriting[listingKey, *provider.ModelListing](ttl),
			Clock:            clock,
		})
	}
	return catalog
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

// current returns the cached listing, loading it once it has expired.
// Concurrent callers share one load and errors are not cached. The load
// ignores the triggering caller's cancellation so it cannot fail the others;
// the provider's timeout bounds it.
func (c *modelCatalog) current(ctx context.Context) (*provider.ModelListing, error) {
	if c.listings == nil {
		return c.load(ctx)
	}
	return c.listings.Get(ctx, listingKey{}, otter.LoaderFunc[listingKey, *provider.ModelListing](
		func(ctx context.Context, _ listingKey) (*provider.ModelListing, error) {
			return c.load(context.WithoutCancel(ctx))
		},
	))
}

func (c *modelCatalog) load(ctx context.Context) (*provider.ModelListing, error) {
	if c.lister == nil {
		return nil, errModelListingUnsupported
	}
	listing, err := c.lister.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	seenAt := c.clock.Now().Unix()
	for _, id := range listing.ModelIDs {
		if _, ok := c.firstSeen[id]; !ok {
			c.firstSeen[id] = seenAt
		}
	}
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
