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

package controlplane

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/rs/zerolog"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ControlPlaneComponentStatus is the JSON shape served by StatusHandler. It
// mirrors the ControlPlaneComponentStatus schema in spec/openapi.yaml.
type ControlPlaneComponentStatus struct {
	ComponentName string `json:"componentName"`
	Namespace     string `json:"namespace"`
	Status        string `json:"status"`
	Timestamp     string `json:"timestamp"`
}

// StatusHandler serves the latest control-plane component health statuses as a
// JSON array (GET /v1/control-plane). It reads them from the control-plane health
// ConfigMap — the same ConfigMap RunHealthChecks writes — where each data key is a
// component name and its value is a YAML statusEntry.
func StatusHandler(k8sClient client.Client) http.Handler {
	namespace := configMapNamespace()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := zerolog.Ctx(r.Context())

		var cm corev1.ConfigMap
		nn := types.NamespacedName{Namespace: namespace, Name: controlPlaneHealthCM}
		if err := k8sClient.Get(r.Context(), nn, &cm); err != nil {
			logger.Error().Err(err).Msgf("failed to read configmap %s/%s", namespace, controlPlaneHealthCM)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		statuses := make([]ControlPlaneComponentStatus, 0, len(cm.Data))
		for name, raw := range cm.Data {
			var entry statusEntry
			if err := yaml.Unmarshal([]byte(raw), &entry); err != nil {
				logger.Error().Err(err).Msgf("failed to parse status for component %s", name)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			statuses = append(statuses, ControlPlaneComponentStatus{
				ComponentName: name,
				Namespace:     entry.Namespace,
				Status:        string(entry.Status),
				Timestamp:     entry.Timestamp.UTC().Format(time.RFC3339),
			})
		}

		// ConfigMap data is an unordered map; sort by name for a stable response.
		sort.Slice(statuses, func(i, j int) bool {
			return statuses[i].ComponentName < statuses[j].ComponentName
		})

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(statuses); err != nil {
			logger.Error().Err(err).Msg("failed to encode control-plane status response")
		}
	})
}
