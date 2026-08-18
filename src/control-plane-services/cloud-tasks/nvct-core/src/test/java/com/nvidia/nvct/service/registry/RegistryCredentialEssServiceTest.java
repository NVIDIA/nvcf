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
package com.nvidia.nvct.service.registry;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.node.StringNode;
import com.nvidia.nvct.service.ess.EssClient;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RegistryCredentialEssServiceTest {

    private static final String NCA_ID = "test-nca-id";
    private static final UUID REGISTRY_CREDENTIAL_ID =
            UUID.fromString("00000000-0000-0000-0000-000000000001");
    private static final String SECRET_NAME = "docker-container-registry-credential";
    private static final String SECRET_VALUE = "dXNlcm5hbWU6ZG9ja2VyLXBhdC1wYXNzd29yZA==";

    @Mock
    private EssClient essClient;

    @InjectMocks
    private RegistryCredentialEssService registryCredentialEssService;

    @Test
    void shouldReturnSecretFromEss() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.of(Map.<String, JsonNode>of(
                        SECRET_NAME, new StringNode(SECRET_VALUE))));

        var secret = registryCredentialEssService
                .getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        assertThat(secret).isPresent();
        assertThat(secret.get().name()).isEqualTo(SECRET_NAME);
        assertThat(secret.get().value().asString()).isEqualTo(SECRET_VALUE);
    }

    @Test
    void shouldReturnEmptyWhenEssHasNoSecret() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.empty());

        var secret = registryCredentialEssService
                .getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        assertThat(secret).isEmpty();
    }

    @Test
    void shouldCacheSecretAndNotCallEssAgain() {
        when(essClient.fetchRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.of(Map.<String, JsonNode>of(
                        SECRET_NAME, new StringNode(SECRET_VALUE))));

        registryCredentialEssService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
        registryCredentialEssService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);

        verify(essClient, times(1))
                .fetchRegistryCredentialSecret(eq(NCA_ID), eq(REGISTRY_CREDENTIAL_ID));
    }
}
