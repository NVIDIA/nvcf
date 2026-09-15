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

package health

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/gorilla/mux"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	HTTPReadinessRoutePath = "/healthz"
)

type LazyReadinessCheckGetter interface {
	SetCheck(c ReadinessStatusGetter)
	GetCheck() (ReadinessStatusGetter, bool)
}

func NewLazyReadinessCheckGetter() LazyReadinessCheckGetter {
	g := &readinessCheckGetter{}
	return g
}

type readinessCheckGetter struct {
	check ReadinessStatusGetter
	mu    sync.RWMutex
}

func (g *readinessCheckGetter) SetCheck(c ReadinessStatusGetter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.check != nil {
		panic("readiness check is already initialized")
	}
	g.check = c
}

func (g *readinessCheckGetter) GetCheck() (ReadinessStatusGetter, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.check, g.check != nil
}

type ReadinessStatusGetter interface {
	GetStatusForLevel(level nvcatypes.StatusLevel) nvcatypes.AgentHealth
}

// HTTPAddReadinessRoute adds a "/healthz" route to the provided router that leverages
// the provided healthchecker
func HTTPAddReadinessRoute(r *mux.Router, g LazyReadinessCheckGetter) {
	r.Path(HTTPReadinessRoutePath).Handler(httpReadinessHandler(g)).Methods(http.MethodGet)
}

func httpReadinessHandler(g LazyReadinessCheckGetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := core.GetLogger(r.Context())

		readinessGetter, ok := g.GetCheck()
		if !ok {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}

		status := readinessGetter.GetStatusForLevel(nvcatypes.StatusLevelWarn)

		if status.Status == nvcatypes.HealthStatusUnhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		if err := json.NewEncoder(w).Encode(status); err != nil {
			log.Error(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})
}
