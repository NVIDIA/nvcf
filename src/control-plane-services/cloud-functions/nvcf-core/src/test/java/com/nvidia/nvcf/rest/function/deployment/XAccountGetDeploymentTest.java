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
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SUPPORTED_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CPU_ARCH;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_DRIVER_VERSION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OS;
import static com.nvidia.nvcf.util.TestConstants.TEST_STORAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_SYSTEM_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.createAutoscalingConfigDto;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.util.List;
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
public class XAccountGetDeploymentTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());

        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testQueueService.clearQueues();
        resetToDefault();
    }

    Stream<Arguments> authDeploymentArgs() {
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10)
                        .backend(GFN).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(8).minInstances(6).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G)
                        .backend(GFN).instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                        .minInstances(7).maxRequestConcurrency(100)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());

        return Stream.of(
                Arguments.of(null, TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID, validGpuSpecs, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("deploy_function"), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_DEPLOYMENT_ID,
                             validGpuSpecs,
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("authDeploymentArgs")
    void shouldListFunctionDeploymentByDeploymentId(
            Object tokenSupplier, String ncaId, UUID deploymentId,
            List<GpuSpecificationDto> ignoredSpecs, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = getToken(tokenSupplier);
        // Get function deployment
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + ncaId
                             + "/deployments/" + deploymentId);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
        responseBody.deployment().deploymentSpecifications().forEach(deploymentSpec -> {
            if (SUPPORTED_INSTANCE_TYPES.contains(deploymentSpec.instanceType())) {
                assertThat(deploymentSpec.cpuArch()).isEqualTo(TEST_CPU_ARCH);
                assertThat(deploymentSpec.os()).isEqualTo(TEST_OS);
                assertThat(deploymentSpec.driverVersion()).isEqualTo(TEST_DRIVER_VERSION);
                assertThat(deploymentSpec.storage()).isEqualTo(TEST_STORAGE);
                assertThat(deploymentSpec.systemMemory()).isEqualTo(TEST_SYSTEM_MEMORY);
                assertThat(deploymentSpec.gpuMemory()).isEqualTo(TEST_GPU_MEMORY);
            }
        });
    }
}
