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

package clustervalidator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The HA workload table names what the stack's releases render.
func TestHAWorkloads_MatchTheStackReleases(t *testing.T) {
	byTier := map[haTier][]string{}
	for _, w := range haWorkloads {
		name := w.namespaceName() + "/" + w.release
		if w.tier == haQuorum {
			name += "/" + w.component
		}
		byTier[w.tier] = append(byTier[w.tier], name)
	}
	assert.ElementsMatch(t, []string{
		"nvcf/api", "api-keys/admin-issuer-proxy", "nvcf/llm-api-gateway", "nvcf/ratelimiter",
		"nats-system/nats-auth-callout-service", "api-keys/api-keys", "sis/sis", "nvcf/notary-service",
		"nvcf/reval", "nvcf/nvct-api", "ess/ess-api",
	}, byTier[haReplicaSafe])
	assert.ElementsMatch(t, []string{"nvcf/grpc-proxy", "nvcf/invocation-service"}, byTier[haSingleton])
	assert.ElementsMatch(t, []string{
		"nats-system/nats/nats", "vault-system/openbao-server/openbao", "cassandra-system/cassandra/cassandra",
	}, byTier[haQuorum])
	for _, w := range haWorkloads {
		if w.tier == haQuorum {
			assert.Contains(t, quorumComponentNames(), w.component, "named as NVCF_EXTERNAL_COMPONENTS names it")
		}
	}
}
