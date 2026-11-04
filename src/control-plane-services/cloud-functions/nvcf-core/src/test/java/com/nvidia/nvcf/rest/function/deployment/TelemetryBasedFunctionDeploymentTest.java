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
package com.nvidia.nvcf.rest.function.deployment;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockNotaryServer;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
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

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class TelemetryBasedFunctionDeploymentTest {
    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;
    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;
    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockNotaryServer.start(notaryBaseUrl, nvcfAudience, nvcfAudience);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_METRICS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.METRICS),
                TEST_TELEMETRY_METRICS_SECRETS
        );
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_TRACES_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.TRACES),
                TEST_TELEMETRY_TRACES_SECRETS
        );
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockEssServer.stop();
        MockIcmsServer.stop();
        MockNotaryServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        testTelemetryService.deleteAllTelemetries();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    Stream<Arguments> shouldTestFunctionDeploymentWithTelemetryArgs() {
        return Stream.of(
                // All telemetry types provided correctly
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.OK
                ),
                // Single telemetry type provided
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        null,
                        null,
                        HttpStatus.OK
                ),
                Arguments.of(
                        null,
                        TEST_TELEMETRY_METRICS_ID,
                        null,
                        HttpStatus.OK
                ),
                Arguments.of(
                        null,
                        null,
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.OK
                ),
                // Two telemetry types provided
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        null,
                        HttpStatus.OK
                ),
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        null,
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.OK
                ),
                Arguments.of(
                        null,
                        TEST_TELEMETRY_METRICS_ID,
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.OK
                ),
                // No telemetry provided
                Arguments.of(
                        null,
                        null,
                        null,
                        HttpStatus.OK
                ),
                // Invalid telemetry IDs
                Arguments.of(
                        UUID.randomUUID(),
                        TEST_TELEMETRY_METRICS_ID,
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.NOT_FOUND
                ),
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        UUID.randomUUID(),
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.NOT_FOUND
                ),
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                Arguments.of(
                        UUID.randomUUID(),
                        UUID.randomUUID(),
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        UUID.randomUUID(),
                        TEST_TELEMETRY_TRACES_ID,
                        HttpStatus.NOT_FOUND
                ),
                Arguments.of(
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                // Completely different UUIDs
                Arguments.of(
                        UUID.randomUUID(),
                        UUID.randomUUID(),
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                )
        );
    }

    @ParameterizedTest
    @MethodSource("shouldTestFunctionDeploymentWithTelemetryArgs")
    void shouldTestFunctionDeploymentWithTelemetry(
            UUID logsTelemetryId,
            UUID metricsTelemetryId,
            UUID tracesTelemetryId,
            HttpStatus expectedStatus) {

        testTelemetryService.createTestFunctionEntityForTelemetry(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_NCA_ID,
                TEST_FUNCTION_NAME,
                FunctionStatus.INACTIVE,
                null,
                logsTelemetryId,
                tracesTelemetryId,
                metricsTelemetryId
        );

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        // Deploy test function
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(8).minInstances(6).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .instanceType(L40G_INSTANCE_TYPE).maxInstances(9).minInstances(7)
                        .maxRequestConcurrency(99).build());

        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(validGpuSpecs).build();

        var requestEntity = RequestEntity.post(
                "/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }
}
