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
	"net/http"
	"slices"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/gorilla/mux"
)

const (
	HTTPLivenessRoutePath = "/livez"
)

type LazyLivenessCheckGetter interface {
	AddChecker(c LivenessProbeHealthChecker)
	GetCheckers() ([]LivenessProbeHealthChecker, bool)
}

func NewLazyLivenessCheckGetter(initial ...LivenessProbeHealthChecker) LazyLivenessCheckGetter {
	g := &livenessCheckGetter{}
	for _, c := range initial {
		g.AddChecker(c)
	}
	return g
}

type livenessCheckGetter struct {
	checks []LivenessProbeHealthChecker
	mu     sync.RWMutex
}

func (g *livenessCheckGetter) AddChecker(c LivenessProbeHealthChecker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks = append(g.checks, c)
}

func (g *livenessCheckGetter) GetCheckers() ([]LivenessProbeHealthChecker, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	// The presence of any single checker is enough to start health checking.
	// The liveness endpoint should not return ok for no health info.
	// The liveness endpoint may temporarily return not-ok once new checks are added,
	// but will eventually transition to ok if the checker's source is healthy.
	return slices.Clone(g.checks), len(g.checks) != 0
}

type LivenessProbeHealthChecker interface {
	StatusOK() bool
	Name() string
}

// HTTPAddLivenessRoute adds a "/livez" route to the provided router that leverages
// the provided healthchecker
func HTTPAddLivenessRoute(r *mux.Router, g LazyLivenessCheckGetter) {
	r.Path(HTTPLivenessRoutePath).Handler(httpLivenessHandler(g)).Methods(http.MethodGet)
}

func httpLivenessHandler(g LazyLivenessCheckGetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := core.GetLogger(r.Context())

		healthCheckers, ok := g.GetCheckers()
		if !ok {
			http.Error(w, "not alive", http.StatusServiceUnavailable)
			return
		}

		for _, healthChk := range healthCheckers {
			if !healthChk.StatusOK() {
				log.WithField("checker", healthChk.Name()).Debugf("Liveness probe check has failed")
				http.Error(w, "liveness probe check has failed\n", http.StatusServiceUnavailable)
				return
			}
		}

		if _, err := w.Write([]byte("ok\n")); err != nil {
			log.Error(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}
