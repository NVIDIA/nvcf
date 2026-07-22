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
package com.nvidia.nvct.service.reval;

import static com.nvidia.nvct.service.reval.RevalClient.CLIENT_REGISTRATION_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_ACCOUNT_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_ARTIFACT_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_NGC_HELM_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestUtil.createHelmBasedTaskEntity;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServerInstanced;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.GpuSpecificationDto;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.registry.RegistryArtifactValidationService;
import com.nvidia.nvct.service.registry.RegistryCredentialService;
import com.nvidia.nvct.service.registry.RegistryTaskMapperService;
import com.nvidia.nvct.util.MockRevalServer;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;

class RevalClientIntegrationTest {

    private static final String REVAL_BASE_URL = "http://localhost:8085";
    private static final String OAUTH_BASE_URL = "http://localhost:8086";
    private static final String CLIENT_ID = "test-client-id";
    private static final String SECRET_ID = "test-secret-id";
    private static final String STATIC_TOKEN = "static-test-token";

    private static final JsonMapper OBJECT_MAPPER = JsonMapper.builder().build();

    private MockOAuth2TokenServerInstanced ssaMockServer;
    private AccountService accountService;
    private SimpleMeterRegistry meterRegistry;
    private RegistryArtifactValidationService registryArtifactValidationService;
    private RegistryCredentialService registryCredentialService;
    private JsonMapper jsonMapper;
    private ManagedHttpResources httpResources;

    @Value("${nvct.sidecars.image-pull-secret}")
    private String sidecarImagePullSecret;
    @Value("${nvct.sidecars.hostname}")
    private String sidecarRegistryHostname;

    @SneakyThrows
    @BeforeEach
    void setUp() {
        this.jsonMapper = JsonMapper.builder().build();
        this.httpResources =
                NvctOAuth2ClientUtils.getClientHttpConnectorManaged(CLIENT_REGISTRATION_ID);

        // Start mock Reval server
        MockRevalServer.start(new URI(REVAL_BASE_URL));

        // Create real OAuth2TokenServerConfigurationProperties
        OAuth2TokenServerConfigurationProperties ssaProperties =
                new OAuth2TokenServerConfigurationProperties(
                        OAUTH_BASE_URL,                                // issuer
                        OAUTH_BASE_URL + "/.well-known/jwks.json",     // keysetUrl
                        "ES256",                                       // jwsAlgorithm
                        null,                                          // serviceBindings
                        List.of(CLIENT_ID),                            // clientBindings
                        null                                           // customBindings
                );

        // Start OAuth mock server
        ssaMockServer = new MockOAuth2TokenServerInstanced(ssaProperties);
        ssaMockServer.start();

        // Mock account service
        accountService = mock(AccountService.class);
        AccountDto account = AccountDto.builder()
                .ncaId(TEST_NCA_ID)
                .name(TEST_ACCOUNT_NAME)
                .registryCredentials(List.of(TEST_DOCKER_CONTAINER_REGISTRY,
                                             TEST_NGC_CONTAINER_REGISTRY,
                                             TEST_NGC_HELM_REGISTRY))
                .lastUpdatedAt(Instant.now())
                .maxTasksAllowed(1)
                .build();
        when(accountService.getAccount(TEST_NCA_ID)).thenReturn(account);
        RegistryMapperService registryMapperService =
                new RegistryMapperService(TEST_CONTAINER_REGISTRY, TEST_ARTIFACT_REGISTRY,
                                          TEST_HELM_REGISTRY);
        RegistryTaskMapperService registryTaskMapperService =
                new RegistryTaskMapperService(registryMapperService);
        registryCredentialService = new RegistryCredentialService(accountService,
                                                                  registryMapperService,
                                                                  registryTaskMapperService,
                                                                  jsonMapper,
                                                                  sidecarImagePullSecret,
                                                                  sidecarRegistryHostname);
        registryArtifactValidationService = mock(RegistryArtifactValidationService.class);
        this.meterRegistry = new SimpleMeterRegistry();
    }

    @AfterEach
    void tearDown() {
        MockRevalServer.stop();

        if (ssaMockServer != null) {
            ssaMockServer.stop();
        }
        if (httpResources != null) {
            httpResources.close();
        }
    }

    @Test
    void testAuthWithStaticToken() {
        // Given
        RevalClient revalClient = createRevalClientWithStaticToken();
        GpuSpecificationDto deploymentInfo = createDeploymentInfo();
        TaskEntity taskEntity =
                createHelmBasedTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                          jsonMapper);
        // When
        revalClient.validate(TEST_NCA_ID, taskEntity, deploymentInfo);

        // Then
        verify(accountService, times(2)).getAccount(TEST_NCA_ID);
    }

    @Test
    void testAuthWithOAuth() {
        // Given
        RevalClient revalClient = createRevalClientWithOAuth();
        GpuSpecificationDto deploymentInfo = createDeploymentInfo();
        TaskEntity taskEntity =
                createHelmBasedTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                          jsonMapper);
        // When
        revalClient.validate(TEST_NCA_ID, taskEntity, deploymentInfo);

        // Then
        // Successful call with OAuth authentication
        verify(accountService, times(2)).getAccount(TEST_NCA_ID);
    }

    private GpuSpecificationDto createDeploymentInfo() {
        ObjectNode configuration = OBJECT_MAPPER.createObjectNode()
                .put("replicas", 3)
                .put("serviceAccountName", "nvct");

        return GpuSpecificationDto.builder()
                .gpu("T10")
                .instanceType("g6.full")
                .configuration(configuration)
                .build();
    }

    private RevalClient createRevalClientWithStaticToken() {
        StaticClientAuthConfiguration.StaticClientRevalProperties staticProperties =
                new StaticClientAuthConfiguration.StaticClientRevalProperties();
        staticProperties.setToken(STATIC_TOKEN);

        return new RevalClient(
                REVAL_BASE_URL,
                true,
                CLIENT_ID,
                SECRET_ID,
                "helmreval:validate",
                OAUTH_BASE_URL + "/token",
                Optional.of(staticProperties),
                registryArtifactValidationService,
                registryCredentialService,
                httpResources,
                WebClient.builder(),
                jsonMapper);
    }

    private RevalClient createRevalClientWithOAuth() {
        return new RevalClient(
                REVAL_BASE_URL,
                true,
                CLIENT_ID,
                SECRET_ID,
                "helmreval:validate",
                OAUTH_BASE_URL + "/token",
                Optional.empty(),
                registryArtifactValidationService,
                registryCredentialService,
                httpResources,
                WebClient.builder(),
                jsonMapper);
    }
}
