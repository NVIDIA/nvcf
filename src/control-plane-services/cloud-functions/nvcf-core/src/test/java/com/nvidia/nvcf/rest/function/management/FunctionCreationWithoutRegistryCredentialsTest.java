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
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_HELM_CHART_WITH_TAG_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_DTOS;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionCreationWithoutRegistryCredentialsTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

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
    }

    @Test
    void shouldCreateContainerFunction() {
        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(TEST_HEALTH_ENDPOINT))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().containerImage())
                .isEqualTo(TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1);
    }

    @Test
    void shouldCreateContainerFunctionWithNgcModelAndResource() {
        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(TEST_HEALTH_ENDPOINT))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .models(TEST_MODEL_DTOS)
                .resources(TEST_RESOURCE_DTOS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().containerImage())
                .isEqualTo(TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1);
        assertThat(responseBody.function().models()).isNotNull();
        assertThat(responseBody.function().models())
                .containsExactlyInAnyOrderElementsOf(TEST_MODEL_DTOS);
        assertThat(responseBody.function().resources()).isNotNull();
        assertThat(responseBody.function().resources()).isEqualTo(TEST_RESOURCE_DTOS);
    }

    @Test
    void shouldCreateHelmFunctionWithNgcModelAndResource() {
        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(TEST_HEALTH_ENDPOINT))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .helmChart(TEST_CUSTOM_HELM_CHART_WITH_TAG_1)
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .models(TEST_MODEL_DTOS)
                .resources(TEST_RESOURCE_DTOS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().helmChart())
                .isEqualTo(TEST_CUSTOM_HELM_CHART_WITH_TAG_1);
        assertThat(responseBody.function().helmChartServiceName())
                .isEqualTo(TEST_HELM_CHART_SERVICE_NAME);
        assertThat(responseBody.function().models()).isNotNull();
        assertThat(responseBody.function().models())
                .containsExactlyInAnyOrderElementsOf(TEST_MODEL_DTOS);
        assertThat(responseBody.function().resources()).isNotNull();
        assertThat(responseBody.function().resources()).isEqualTo(TEST_RESOURCE_DTOS);
    }

    @Test
    void shouldCreateHelmFunction() {
        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(TEST_HEALTH_ENDPOINT))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .helmChart(TEST_CUSTOM_HELM_CHART_WITH_TAG_1)
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().helmChart())
                .isEqualTo(TEST_CUSTOM_HELM_CHART_WITH_TAG_1);
        assertThat(responseBody.function().helmChartServiceName())
                .isEqualTo(TEST_HELM_CHART_SERVICE_NAME);
    }

}
