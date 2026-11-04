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
package com.nvidia.nvcf.service.reval;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.service.reval.RevalClient.CLIENT_REGISTRATION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_CHART;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY_CREDENTIAL;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServerInstanced;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientRevalProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonAuthDto;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonDto;
import com.nvidia.nvcf.rest.registry.dto.K8sSecretsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.service.registry.RegistryArtifactValidationService;
import com.nvidia.nvcf.service.registry.RegistryCredentialFunctionService;
import com.nvidia.nvcf.util.MockRevalServer;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.net.URI;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;

class RevalClientIntegrationTest {

    private static final String REVAL_BASE_URL = "http://localhost:8085";
    private static final String OAUTH_BASE_URL = "http://localhost:8086";
    private static final String CLIENT_ID = "test-client-id";
    private static final String SECRET_ID = "test-secret-id";
    private static final String STATIC_TOKEN = "static-test-token";
    private static final String TEST_NCA_ID = "test-nca-id";
    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();

    private MockRevalServer mockRevalServer;
    private MockOAuth2TokenServerInstanced jwtMockServer;
    private RevalMetrics revalMetrics;
    private SimpleMeterRegistry meterRegistry;
    private RegistryArtifactValidationService registryArtifactValidationService;
    private RegistryCredentialFunctionService registryCredentialFunctionService;
    private JsonMapper jsonMapper;
    private ManagedHttpResources httpResources;

    @SneakyThrows
    @BeforeEach
    void setUp() {
        this.jsonMapper = new JsonMapper();
        this.httpResources =
                NvcfOAuth2ClientUtils.getClientHttpConnectorManaged(CLIENT_REGISTRATION_ID);

        // Start mock Reval server
        mockRevalServer = new MockRevalServer(new URI(REVAL_BASE_URL));
        mockRevalServer.start();

        // Create real OAuth2TokenServerConfigurationProperties
        OAuth2TokenServerConfigurationProperties jwtProperties =
                new OAuth2TokenServerConfigurationProperties(
                        OAUTH_BASE_URL,                                // issuer
                        OAUTH_BASE_URL + "/.well-known/jwks.json",     // keysetUrl
                        "ES256",                                       // jwsAlgorithm
                        null,                                          // serviceBindings
                        List.of(CLIENT_ID),                            // clientBindings
                        null                                           // customBindings
                );

        // Start OAuth mock server
        jwtMockServer = new MockOAuth2TokenServerInstanced(jwtProperties);
        jwtMockServer.start();

        registryArtifactValidationService = mock(RegistryArtifactValidationService.class);
        registryCredentialFunctionService = mock(RegistryCredentialFunctionService.class);
        var imageRegistryAuthConfig = getRegistryCredentials(List.of(
                TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL));
        var helmRegistryAuthConfig = getRegistryCredentials(List.of(
                TEST_NGC_HELM_REGISTRY_CREDENTIAL));
        when(registryCredentialFunctionService.getContainerRegistryImagePullSecrets(
                any(FunctionEntity.class)))
                .thenReturn(imageRegistryAuthConfig);
        when(registryCredentialFunctionService
                     .getHelmRegistryImagePullSecrets(any(FunctionEntity.class)))
                .thenReturn(helmRegistryAuthConfig);

        this.meterRegistry = new SimpleMeterRegistry();
        this.revalMetrics = new RevalMetrics(meterRegistry);
    }

    @AfterEach
    void tearDown() {
        if (mockRevalServer != null) {
            mockRevalServer.stop();
        }

        if (jwtMockServer != null) {
            jwtMockServer.stop();
        }
    }

    @Test
    void testValidateIncrementsMetrics() {
        // Given
        RevalClient service = createServiceWithStaticToken();
        GpuSpecificationDto deploymentInfo = createDeploymentInfo();
        FunctionEntity functionEntity = createHelmBasedFunction();

        // When
        service.validate(TEST_NCA_ID, functionEntity, deploymentInfo);

        // Then
        // Counter has been increased
        var counter = (Counter) meterRegistry.getMeters().getFirst();
        assertThat(counter.count()).isEqualTo(1.0);

        // Counter has the right name / tags
        var metricId = counter.getId();
        assertThat(metricId.getName()).isEqualTo("nvcf.service.reval.counter");
        assertThat(metricId.getTag(TAG_NCA_ID)).isEqualTo(TEST_NCA_ID);
        assertThat(metricId.getTag("status")).isEqualTo("200");
    }

    @Test
    void testAuthWithStaticToken() {
        // Given
        RevalClient service = createServiceWithStaticToken();
        GpuSpecificationDto deploymentInfo = createDeploymentInfo();
        FunctionEntity functionEntity = createHelmBasedFunction();

        // When
        service.validate(TEST_NCA_ID, functionEntity, deploymentInfo);

        // Then
        // Successful call with static token authentication
        verify(registryCredentialFunctionService).getHelmRegistryImagePullSecrets(functionEntity);
        verify(registryCredentialFunctionService).getContainerRegistryImagePullSecrets(functionEntity);
    }

    @Test
    void testAuthWithOAuth() {
        // Given
        RevalClient service = createServiceWithOAuth();
        GpuSpecificationDto deploymentInfo = createDeploymentInfo();
        FunctionEntity functionEntity = createHelmBasedFunction();

        // When
        service.validate(TEST_NCA_ID, functionEntity, deploymentInfo);

        // Then
        // Successful call with OAuth authentication
        verify(registryCredentialFunctionService).getHelmRegistryImagePullSecrets(functionEntity);
        verify(registryCredentialFunctionService).getContainerRegistryImagePullSecrets(functionEntity);
    }

    private GpuSpecificationDto createDeploymentInfo() {
        ObjectNode configuration = OBJECT_MAPPER.createObjectNode()
                .put("replicas", 3)
                .put("serviceAccountName", "nvcf");

        return GpuSpecificationDto.builder()
                .gpuSpecificationId(TEST_GPU_SPEC_ID)
                .gpu("Tesla T4")
                .instanceType("g5.2xlarge")
                .maxInstances(5)
                .minInstances(2)
                .configuration(configuration)
                .build();
    }

    private RevalClient createServiceWithStaticToken() {
        StaticClientRevalProperties staticProperties = new StaticClientRevalProperties();
        staticProperties.setToken(STATIC_TOKEN);

        return new RevalClient(
                REVAL_BASE_URL,
                CLIENT_ID,
                SECRET_ID,
                "helmreval:validate",
                OAUTH_BASE_URL + "/token",
                true,
                Collections.emptySet(),
                Optional.of(staticProperties),
                revalMetrics,
                registryArtifactValidationService,
                registryCredentialFunctionService,
                jsonMapper,
                WebClient.builder(),
                httpResources
        );
    }

    private RevalClient createServiceWithOAuth() {
        return new RevalClient(
                REVAL_BASE_URL,
                CLIENT_ID,
                SECRET_ID,
                "helmreval:validate",
                OAUTH_BASE_URL + "/token",
                true,
                Collections.emptySet(),
                Optional.empty(),
                revalMetrics,
                registryArtifactValidationService,
                registryCredentialFunctionService,
                jsonMapper,
                WebClient.builder(),
                httpResources
        );
    }

    private FunctionEntity createHelmBasedFunction() {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .helmChart(TEST_NGC_HELM_CHART.toString())
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .createdAt(Instant.now())
                .build();
    }

    private K8sSecretsDto getRegistryCredentials(
            List<RegistryCredentialDto> registryCredentialDtos) {
        K8sSecretsDto k8SSecretsDto = K8sSecretsDto.builder().k8sSecrets(new ArrayList<>()).build();
        registryCredentialDtos.forEach(dto -> {
            var hostname = dto.registryHostname();
            var secret = dto.secret().value().asString();
            var dockerConfigJsonRegistryCredentialDto = DockerConfigJsonAuthDto
                    .builder()
                    .auth(secret)
                    .build();
            k8SSecretsDto.k8sSecrets().add(
                    DockerConfigJsonDto.builder().auths(
                            Map.of(hostname, dockerConfigJsonRegistryCredentialDto)).build());
        });
        return k8SSecretsDto;
    }
}
