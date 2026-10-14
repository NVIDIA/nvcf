// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// recipeCatalogPath names the recipe catalog file: the recipe charts'
// published index.json, mounted from a ConfigMap (helm/values.yaml). It is
// read again whenever it changes, so updating the ConfigMap adds recipes
// without a restart once the kubelet syncs the volume, within about a minute.
// The mount must not use subPath, which never updates.
const recipeCatalogPath = "RECIPE_CATALOG_PATH"

// catalogSchemaVersion is the index.json format the UI reads (spec/bff-openapi.yaml).
const catalogSchemaVersion = 1

// errNoCatalog means there is no valid catalog to serve: none is configured,
// the file is missing, or it hasn't been valid since the BFF started.
var errNoCatalog = errors.New("recipe catalog unavailable")

// recipeCatalog serves the catalog file, reading it again when its
// modification time or size changes. A file that turns invalid keeps the last
// valid catalog, so a bad edit doesn't take the recipes page down.
type recipeCatalog struct {
	path   string
	logger zerolog.Logger

	mu sync.Mutex
	// The file version last read, valid or not, so each version is read and
	// reported once.
	seen     bool
	seenMod  time.Time
	seenSize int64
	// body is the last valid catalog, compacted.
	body []byte
}

func newRecipeCatalog(path string, logger zerolog.Logger) *recipeCatalog {
	return &recipeCatalog{path: path, logger: logger.With().Str("catalog", path).Logger()}
}

// current returns the catalog to serve, or errNoCatalog.
func (c *recipeCatalog) current() ([]byte, error) {
	if c.path == "" {
		return nil, errNoCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		if c.body != nil {
			c.logger.Warn().Msg("Recipe catalog removed")
		}
		c.seen, c.body = false, nil
		return nil, errNoCatalog
	}
	if err != nil {
		c.logger.Error().Err(err).Msg("Can't read the recipe catalog")
		return c.last()
	}
	if c.seen && info.ModTime().Equal(c.seenMod) && info.Size() == c.seenSize {
		return c.last()
	}
	c.seen, c.seenMod, c.seenSize = true, info.ModTime(), info.Size()

	data, err := os.ReadFile(c.path)
	if err != nil {
		c.logger.Error().Err(err).Msg("Can't read the recipe catalog")
		return c.last()
	}
	body, recipes, err := parseRecipeCatalog(data)
	if err != nil {
		event := c.logger.Error().Err(err)
		if c.body != nil {
			event.Msg("Invalid recipe catalog; still serving the previous one")
		} else {
			event.Msg("Invalid recipe catalog")
		}
		return c.last()
	}
	c.body = body
	c.logger.Info().Int("recipes", recipes).Msg("Loaded recipe catalog")
	return c.body, nil
}

func (c *recipeCatalog) last() ([]byte, error) {
	if c.body == nil {
		return nil, errNoCatalog
	}
	return c.body, nil
}

// parseRecipeCatalog checks what the UI relies on, and returns the catalog
// compacted with its number of recipes. Everything else passes through, so
// the export can grow fields without a BFF change.
func parseRecipeCatalog(data []byte) ([]byte, int, error) {
	var catalog struct {
		SchemaVersion int `json:"schemaVersion"`
		Recipes       []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"recipes"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, 0, fmt.Errorf("not a catalog: %w", err)
	}
	if catalog.SchemaVersion != catalogSchemaVersion {
		return nil, 0, fmt.Errorf("schemaVersion %d, want %d", catalog.SchemaVersion, catalogSchemaVersion)
	}
	if catalog.Recipes == nil {
		return nil, 0, errors.New("no recipes array")
	}
	ids := make(map[string]bool, len(catalog.Recipes))
	for i, r := range catalog.Recipes {
		if r.ID == "" || r.Name == "" {
			return nil, 0, fmt.Errorf("recipe %d has no id or name", i)
		}
		if ids[r.ID] {
			return nil, 0, fmt.Errorf("recipe id %q is listed twice", r.ID)
		}
		ids[r.ID] = true
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, 0, err
	}
	return compact.Bytes(), len(catalog.Recipes), nil
}

// registerRecipes serves the catalog at GET /api/v1/recipes.
func registerRecipes(router *http.ServeMux, catalog *recipeCatalog) {
	router.HandleFunc("GET /api/v1/recipes", func(w http.ResponseWriter, _ *http.Request) {
		body, err := catalog.current()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Recipe catalog unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Revalidate on every load: the catalog changes whenever its ConfigMap does.
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}
