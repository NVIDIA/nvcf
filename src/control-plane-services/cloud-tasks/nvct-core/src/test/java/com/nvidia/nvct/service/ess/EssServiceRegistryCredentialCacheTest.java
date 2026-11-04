/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.nvct.service.ess;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mockito;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.node.StringNode;

// Unit tests for the dedicated registry credential secret cache in EssService.
class EssServiceRegistryCredentialCacheTest {

    private static final String CACHE_NAME = "nvctRegistryCredentialSecrets";
    private static final String NCA_ID = "test-nca-id";
    private static final UUID REGISTRY_CREDENTIAL_ID =
            UUID.fromString("11111111-0000-0000-0000-000000000001");

    private EssClient essClient;
    private SimpleMeterRegistry meterRegistry;
    private EssService essService;

    @BeforeEach
    void setUp() {
        essClient = Mockito.mock(EssClient.class);
        meterRegistry = new SimpleMeterRegistry();
        essService = new EssService(essClient, Duration.ofMinutes(5), 3072L, meterRegistry);
    }

    @Test
    void servesSecretFromCacheAndCallsEssOnce() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.of(secretResponse("cred", "secret-value")));

        var first = essService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
        var second = essService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        assertThat(first).isPresent();
        assertThat(first.get().name()).isEqualTo("cred");
        assertThat(first.get().value().asString()).isEqualTo("secret-value");
        assertThat(second).isPresent();
        // The second read is served from the cache, so ESS is queried only once.
        verify(essClient, times(1)).fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
    }

    @Test
    void doesNotCacheMissingSecretSoNextReadRetriesEss() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.empty())
                .thenReturn(Optional.of(secretResponse("cred", "secret-value")));

        var first = essService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
        var second = essService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        assertThat(first).isEmpty();
        assertThat(second).isPresent();
        // A missing secret is not cached, so the second read hits ESS again.
        verify(essClient, times(2)).fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
    }

    @Test
    void publishesCacheMetrics() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.of(secretResponse("cred", "secret-value")));

        essService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        assertThat(meterRegistry.getMeters())
                .anyMatch(meter -> CACHE_NAME.equals(meter.getId().getTag("cache")));
    }

    private static Map<String, JsonNode> secretResponse(String name, String value) {
        return Map.of(name, new StringNode(value));
    }
}
