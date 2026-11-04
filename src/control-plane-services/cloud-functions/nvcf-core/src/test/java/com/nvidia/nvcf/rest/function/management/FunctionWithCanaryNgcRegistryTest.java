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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_CHART_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_WITH_CANARY_HOST_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.ArtifactDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionWithCanaryNgcRegistryTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    protected JsonMapper jsonMapper;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    Stream<Arguments> functionCreateArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST,
                             null,
                             TEST_MODEL_URL_WITH_CANARY_HOST,
                             TEST_RESOURCE_URL_WITH_CANARY_HOST_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST,
                             null,
                             TEST_MODEL_URL_WITH_CANARY_HOST,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_WITH_CANARY_HOST_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             null,
                             TEST_NGC_HELM_CHART_WITH_CANARY_HOST,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_WITH_CANARY_HOST_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("functionCreateArgs")
    void shouldCreateFunction(
            String token, URI containerImage, URI helmChart,
            String modelUri, String resourceUri, String healthUri, Set<String> tags,
            HttpStatus expectedStatus) {
        var model1Name = "model-1";
        var model1Version = "1.0";
        var modelDtos = List.of(FunctionModelDto.builder().name(model1Name)
                                        .version(model1Version).uri(URI.create(modelUri)).build());
        HealthDto healthDto = null;
        if (healthUri != null) {
            healthDto = HealthDto.builder()
                    .expectedStatusCode(EXPECTED_STATUS_CODE)
                    .timeout(HEALTH_TIMEOUT)
                    .port(TEST_INFERENCE_PORT)
                    .protocol(ProtocolEnum.HTTP)
                    .uri(URI.create(healthUri))
                    .build();
        }
        var requestBodyBuilder = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .tags(tags)
                .description(TEST_DESCRIPTION)
                .health(healthDto);

        var requestBody = containerImage == null ?
                requestBodyBuilder
                        .helmChart(helmChart)
                        .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                        .build() :
                requestBodyBuilder
                        .containerArgs(TEST_CONTAINER_ARGS)
                        .containerImage(containerImage)
                        .build();
        Set<ArtifactDto> resourceDtos = null;
        var resource1Name = "resource-1";
        var resource1Version = "1.0";
        if (resourceUri != null) {
            resourceDtos = Set.of(
                    ArtifactDto.builder().name(resource1Name)
                            .version(resource1Version)
                            .uri(URI.create(resourceUri))
                            .build());
            requestBody.setResources(resourceDtos);
        }
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().versionId()).isNotNull();
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().apiBodyFormat()).isEqualTo(PREDICT_V2);
        if (containerImage != null) {
            assertThat(responseBody.function().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
            assertThat(responseBody.function().containerImage()).isEqualTo(containerImage);
        }
        if (helmChart != null) {
            assertThat(responseBody.function().helmChart()).isEqualTo(helmChart);
            assertThat(responseBody.function().helmChartServiceName())
                    .isEqualTo(TEST_HELM_CHART_SERVICE_NAME);
        }
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        assertThat(responseBody.function().models()).isNotNull().hasSize(1);
        assertThat(responseBody.function().models()).containsExactlyInAnyOrderElementsOf(modelDtos);
        assertThat(responseBody.function().resources()).isNotNull().hasSize(1);
        assertThat(responseBody.function().resources()).isEqualTo(resourceDtos);
        assertThat(responseBody.function().tags()).isEqualTo(tags);
        assertThat(responseBody.function().description()).isEqualTo(TEST_DESCRIPTION);
        assertThat(responseBody.function().health()).isNotNull();
        if (healthUri != null) {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    ProtocolEnum.HTTP);
            assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        } else {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    ProtocolEnum.HTTP);
            assertThat(responseBody.function().healthUri()).isEqualTo(DEFAULT_HEALTH_ENDPOINT);
            assertThat(responseBody.function().health().getUri()).isEqualTo(
                    DEFAULT_HEALTH_ENDPOINT);
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        }
    }
}
