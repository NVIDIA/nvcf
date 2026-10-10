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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// noRecipes is a BFF with no catalog configured.
var noRecipes = newRecipeCatalog("", zerolog.Nop())

// catalogFixture is the recipe charts' index.json (NVIDIA/nvcf#2340 at
// 3c8d1a4), passed through helm/recipes/catalog.jq as the ConfigMap is.
const catalogFixture = "testdata/recipes.json"

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(catalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func compacted(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// writeCatalog writes data to path with a modification time later than any
// earlier write, so the change is seen even within one clock tick.
func writeCatalog(t *testing.T, path, data string, version int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1_790_000_000+int64(version), 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func catalogWith(ids ...string) string {
	recipes := make([]string, 0, len(ids))
	for _, id := range ids {
		recipes = append(recipes, `{"id":"`+id+`","name":"`+id+`","availability":{"status":"planned","deployable":false},"profiles":[]}`)
	}
	return `{"schemaVersion":1,"recipes":[` + strings.Join(recipes, ",") + `]}`
}

func TestParseRecipeCatalog(t *testing.T) {
	body, recipes, err := parseRecipeCatalog(readFixture(t))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if recipes != 8 || bytes.ContainsRune(body, '\n') {
		t.Errorf("got %d recipes, compacted=%t", recipes, !bytes.ContainsRune(body, '\n'))
	}
	if _, n, err := parseRecipeCatalog([]byte(`{"schemaVersion":1,"recipes":[]}`)); err != nil || n != 0 {
		t.Errorf("an empty catalog is valid: n=%d err=%v", n, err)
	}

	for name, data := range map[string]string{
		"not JSON":       `{"schemaVersion":1,`,
		"other version":  `{"schemaVersion":2,"recipes":[]}`,
		"no version":     `{"recipes":[]}`,
		"no recipes":     `{"schemaVersion":1}`,
		"null recipes":   `{"schemaVersion":1,"recipes":null}`,
		"recipe no id":   `{"schemaVersion":1,"recipes":[{"name":"Qwen"}]}`,
		"recipe no name": `{"schemaVersion":1,"recipes":[{"id":"qwen"}]}`,
		"duplicate id":   catalogWith("qwen", "qwen"),
		"not an object":  `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseRecipeCatalog([]byte(data)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestRecipeCatalogReadsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	var logs bytes.Buffer
	catalog := newRecipeCatalog(path, zerolog.New(&logs))

	if _, err := catalog.current(); !errors.Is(err, errNoCatalog) {
		t.Fatalf("missing file: %v", err)
	}

	writeCatalog(t, path, catalogWith("a"), 1)
	if body, err := catalog.current(); err != nil || !strings.Contains(string(body), `"id":"a"`) {
		t.Fatalf("first catalog: %s %v", body, err)
	}

	writeCatalog(t, path, catalogWith("a", "b"), 2)
	if body, _ := catalog.current(); !strings.Contains(string(body), `"id":"b"`) {
		t.Fatalf("an updated file is read again: %s", body)
	}

	// A bad edit keeps the last valid catalog, and is reported once.
	writeCatalog(t, path, `{"schemaVersion":1,`, 3)
	for range 3 {
		if body, err := catalog.current(); err != nil || !strings.Contains(string(body), `"id":"b"`) {
			t.Fatalf("after an invalid edit: %s %v", body, err)
		}
	}
	if n := strings.Count(logs.String(), "still serving the previous one"); n != 1 {
		t.Errorf("invalid edit logged %d times:\n%s", n, logs.String())
	}

	writeCatalog(t, path, catalogWith("c"), 4)
	if body, _ := catalog.current(); !strings.Contains(string(body), `"id":"c"`) || strings.Contains(string(body), `"id":"b"`) {
		t.Fatalf("recovers on the next valid edit: %s", body)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.current(); !errors.Is(err, errNoCatalog) {
		t.Fatalf("removed file: %v", err)
	}
}

func TestRecipeCatalogNeverValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	writeCatalog(t, path, `{"schemaVersion":2,"recipes":[]}`, 1)
	if _, err := newRecipeCatalog(path, zerolog.Nop()).current(); !errors.Is(err, errNoCatalog) {
		t.Fatalf("got %v", err)
	}
	if _, err := noRecipes.current(); !errors.Is(err, errNoCatalog) {
		t.Fatalf("unset path: %v", err)
	}
}

// The kubelet updates a ConfigMap volume by pointing its ..data symlink at a
// new directory; the file the BFF opens is a symlink through ..data.
func TestRecipeCatalogFollowsConfigMapUpdates(t *testing.T) {
	dir := t.TempDir()
	version := func(name, data string, n int) {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		writeCatalog(t, filepath.Join(dir, name, "index.json"), data, n)
	}
	point := func(target string) {
		tmp := filepath.Join(dir, "..data_tmp")
		if err := os.Symlink(target, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
			t.Fatal(err)
		}
	}
	version("..2026_10_08_01", catalogWith("a"), 1)
	point("..2026_10_08_01")
	path := filepath.Join(dir, "index.json")
	if err := os.Symlink(filepath.Join("..data", "index.json"), path); err != nil {
		t.Fatal(err)
	}

	catalog := newRecipeCatalog(path, zerolog.Nop())
	if body, err := catalog.current(); err != nil || !strings.Contains(string(body), `"id":"a"`) {
		t.Fatalf("before the update: %s %v", body, err)
	}
	version("..2026_10_08_02", catalogWith("a", "b"), 2)
	point("..2026_10_08_02")
	if body, err := catalog.current(); err != nil || !strings.Contains(string(body), `"id":"b"`) {
		t.Fatalf("after the update: %s %v", body, err)
	}
}

func TestRecipesRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	fixture := readFixture(t)
	writeCatalog(t, path, string(fixture), 1)
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, newRecipeCatalog(path, zerolog.Nop()))

	rec := serve(h, http.MethodGet, "/api/v1/recipes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), compacted(t, fixture)) {
		t.Error("body isn't the catalog")
	}
	assertMatchesSpec(t, http.MethodGet, "/api/v1/recipes", "200", rec.Body.Bytes())

	if rec := serve(h, http.MethodPost, "/api/v1/recipes"); rec.Code != http.StatusNotFound ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("POST: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestRecipesRouteWithoutCatalog(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	rec := serve(h, http.MethodGet, "/api/v1/recipes")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"Recipe catalog unavailable"}` {
		t.Errorf("body %s", got)
	}
	assertMatchesSpec(t, http.MethodGet, "/api/v1/recipes", "503", rec.Body.Bytes())
}
