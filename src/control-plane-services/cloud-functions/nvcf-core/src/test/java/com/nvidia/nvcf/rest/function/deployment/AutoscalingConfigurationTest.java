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
import static com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationPolicyEnum.PLATFORM_CONFIGURATION;
import static com.nvidia.nvcf.service.function.AutoscalingConfigurationMapper.toAutoscalingConfigurationProto;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.createAutoscalingConfigDto;
import static com.nvidia.nvcf.util.TestUtil.readFileAsString;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.nio.ByteBuffer;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
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
class AutoscalingConfigurationTest {

    private static final String CONFIG_DEPLOYMENT_TEMPLATE_TXT =
            "fixtures/autoscaler/config/deployment_template.txt";
    private static final String CONFIG_UPDATE_GPU_SPEC_TEMPLATE_TXT =
            "fixtures/autoscaler/config/update_gpu_spec_template.txt";
    // Valid configs (200 OK)
    private static final String CONFIG_FULL_JSON =
            "fixtures/autoscaler/config/valid_full_config.json";
    private static final String CONFIG_NO_STICKINESS_JSON =
            "fixtures/autoscaler/config/valid_no_stickiness.json";
    private static final String CONFIG_ONLY_SCALE_DOWN_JSON =
            "fixtures/autoscaler/config/valid_scale_down_only.json";
    private static final String CONFIG_ONLY_SCALE_UP_JSON =
            "fixtures/autoscaler/config/valid_scale_up_only.json";
    private static final String CONFIG_NO_STICKINESS_BOTH_JSON =
            "fixtures/autoscaler/config/valid_both_no_stickiness.json";
    // Invalid configs (400 Bad Request)
    private static final String CONFIG_STICKINESS_MISSING_THRESHOLD_JSON =
            "fixtures/autoscaler/config/invalid_stickiness_missing_threshold.json";
    private static final String CONFIG_STICKINESS_THRESHOLD_GTE_SIZE_JSON =
            "fixtures/autoscaler/config/invalid_stickiness_threshold_gte_size.json";
    private static final String CONFIG_STICKINESS_SIZE_GTE_1H_JSON =
            "fixtures/autoscaler/config/invalid_stickiness_size_gt_1h.json";
    private static final String CONFIG_MISSING_THRESHOLD_JSON =
            "fixtures/autoscaler/config/invalid_missing_threshold.json";
    private static final String CONFIG_NEGATIVE_THRESHOLD_JSON =
            "fixtures/autoscaler/config/invalid_negative_threshold.json";
    private static final String CONFIG_MISSING_FACTOR_JSON =
            "fixtures/autoscaler/config/invalid_missing_factor.json";
    private static final String CONFIG_SCALE_UP_FACTOR_LTE_1_JSON =
            "fixtures/autoscaler/config/invalid_scale_up_factor_lte_1.json";
    private static final String CONFIG_SCALE_DOWN_FACTOR_GTE_1_JSON =
            "fixtures/autoscaler/config/invalid_scale_down_factor_gte_1.json";

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

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


    Stream<Arguments> updateAutoscalingConfigArgs() {
        var adjustedAutoscalingConfig = createAutoscalingConfigDto("invocations");
        return Stream.of(
                // 1. empty initial config
                Arguments.of(Map.of(), TEST_GPU_SPEC_ID, createAutoscalingConfigDto()),
                // 2. initial config is the same as updating
                Arguments.of(Map.of(TEST_GPU_SPEC_ID, ByteBuffer.wrap(
                                     toAutoscalingConfigurationProto(
                                             createAutoscalingConfigDto()).toByteArray())),
                             TEST_GPU_SPEC_ID,
                             createAutoscalingConfigDto()),
                // 3. initial config has changed
                Arguments.of(Map.of(TEST_GPU_SPEC_ID, ByteBuffer.wrap(
                                     toAutoscalingConfigurationProto(
                                             createAutoscalingConfigDto()).toByteArray())),
                             TEST_GPU_SPEC_ID,
                             adjustedAutoscalingConfig),
                // 4. adding new config
                Arguments.of(Map.of(TEST_GPU_SPEC_ID_2, ByteBuffer.wrap(
                                     toAutoscalingConfigurationProto(
                                             createAutoscalingConfigDto()).toByteArray())),
                             TEST_GPU_SPEC_ID,
                             adjustedAutoscalingConfig),
                // 5. should not delete if empty
                Arguments.of(Map.of(TEST_GPU_SPEC_ID, ByteBuffer.wrap(
                                            toAutoscalingConfigurationProto(
                                                    createAutoscalingConfigDto()).toByteArray()),
                                    TEST_FUNCTION_ID_2, toAutoscalingConfigurationProto(
                                             createAutoscalingConfigDto()).toByteArray()),
                             TEST_GPU_SPEC_ID,
                             null)
        );
    }

    @ParameterizedTest
    @MethodSource("updateAutoscalingConfigArgs")
    void shouldUpdateAutoscalingConfig(Map<UUID, ByteBuffer> initialConfig,
                                      UUID gpuSpecId,
                                      AutoscalingConfigurationDto updateConfig) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        var gpuSpecs = Set.of(GpuSpecificationEntity.builder()
                                      .key(GpuSpecificationKey.builder()
                                                   .ncaId(TEST_NCA_ID)
                                                   .deploymentId(TEST_DEPLOYMENT_ID)
                                                   .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                   .build())
                                      .instanceType(T10_INSTANCE_TYPE)
                                      .gpu(T10).maxInstances(5).minInstances(0)
                                      .build());
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                gpuSpecs, initialConfig);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update function deployment using GPU/Backend specs that are different from the
        // ones that were used when the function was originally deployed.
        var requestBody = UpdateGpuSpecificationRequest.builder()
                .minInstances(3)
                .autoscalingConfiguration(updateConfig).build();
        var requestEntity = RequestEntity
                .patch("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + gpuSpecId)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(
                requestEntity, UpdateGpuSpecificationResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        UpdateGpuSpecificationResponse response = responseEntity.getBody();
        assertThat(response).isNotNull();
        var updatedConfig = response.gpuSpecification().autoscalingConfiguration();
        var actual = toAutoscalingConfigurationProto(updatedConfig);
        if (updateConfig != null) {
            var expected = toAutoscalingConfigurationProto(updateConfig);
            assertThat(actual).isEqualTo(expected);
        } else {
            // if update was null, we should not delete existing one
            var expected = toAutoscalingConfigurationProto(createAutoscalingConfigDto());
            assertThat(actual).isEqualTo(expected);
        }
    }

    Stream<Arguments> autoscalingConfigurationArgs() {
        return Stream.of(
                // 1. full config with both scaleUpDetails and scaleDownDetails: 200
                Arguments.of(readFileAsString(CONFIG_FULL_JSON), HttpStatus.OK),
                // 2. no stickiness window in either: 200
                Arguments.of(readFileAsString(CONFIG_NO_STICKINESS_JSON), HttpStatus.OK),
                // 3. only scaleDownDetails (no scaleUpDetails): 200
                Arguments.of(readFileAsString(CONFIG_ONLY_SCALE_DOWN_JSON), HttpStatus.OK),
                // 4. only scaleUpDetails (no scaleDownDetails): 200
                Arguments.of(readFileAsString(CONFIG_ONLY_SCALE_UP_JSON), HttpStatus.OK),
                // 5. both details without stickiness: 200
                Arguments.of(readFileAsString(CONFIG_NO_STICKINESS_BOTH_JSON), HttpStatus.OK),
                // 6. stickiness missing threshold: 400
                Arguments.of(readFileAsString(CONFIG_STICKINESS_MISSING_THRESHOLD_JSON),
                             HttpStatus.BAD_REQUEST),
                // 7. stickiness threshold >= size: 400
                Arguments.of(readFileAsString(CONFIG_STICKINESS_THRESHOLD_GTE_SIZE_JSON),
                             HttpStatus.BAD_REQUEST),
                // 8. stickiness size >= 1 hour: 400
                Arguments.of(readFileAsString(CONFIG_STICKINESS_SIZE_GTE_1H_JSON),
                             HttpStatus.BAD_REQUEST),
                // 9. scaleUpDetails missing threshold: 400
                Arguments.of(readFileAsString(CONFIG_MISSING_THRESHOLD_JSON),
                             HttpStatus.BAD_REQUEST),
                // 10. scaleUpDetails negative threshold: 400
                Arguments.of(readFileAsString(CONFIG_NEGATIVE_THRESHOLD_JSON),
                             HttpStatus.BAD_REQUEST),
                // 11. scaleUpDetails missing factor: 400
                Arguments.of(readFileAsString(CONFIG_MISSING_FACTOR_JSON), HttpStatus.BAD_REQUEST),
                // 12. scaleUpDetails factor <= 1.0: 400
                Arguments.of(readFileAsString(CONFIG_SCALE_UP_FACTOR_LTE_1_JSON),
                             HttpStatus.BAD_REQUEST),
                // 13. scaleDownDetails factor >= 1.0: 400
                Arguments.of(readFileAsString(CONFIG_SCALE_DOWN_FACTOR_GTE_1_JSON),
                             HttpStatus.BAD_REQUEST)
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("autoscalingConfigurationArgs")
    void shouldCreateFunctionDeploymentWithAutoscalingConfig(
            String configPayload,
            HttpStatus expectedStatus) {
        var deploymentDtoTemplate = readFileAsString(CONFIG_DEPLOYMENT_TEMPLATE_TXT);
        var payload = deploymentDtoTemplate.formatted(configPayload);
        // Create functions in different accounts.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        var requestEntity = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                                       + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes());

        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (HttpStatus.OK.equals(expectedStatus)) {
            var responseBody = responseEntity.getBody();
            var deployment = jsonMapper
                    .readValue(responseBody, DeploymentResponse.class)
                    .deployment();
            var deploymentTree = jsonMapper.readTree(responseBody);
            deployment.deploymentSpecifications()
                    .forEach(gpuSpec -> {
                        var autoscalingConfiguration = gpuSpec.autoscalingConfiguration();
                        if (autoscalingConfiguration != null) {
                            if (autoscalingConfiguration.scaleDownDetails() != null) {
                                var scaleDownDetails = autoscalingConfiguration.scaleDownDetails();
                                if (scaleDownDetails.stickiness() != null) {
                                    var sw = scaleDownDetails.stickiness();
                                    assertThat(sw.size().toString()).hasToString("PT30M");
                                    assertThat(sw.threshold().toString()).hasToString("PT5M");
                                    var size = deploymentTree.get("deployment")
                                            .get("deploymentSpecifications").get(0)
                                            .get("autoscalingConfiguration").get("scaleDownDetails")
                                            .get("stickiness").get("size").asString();
                                    var threshold = deploymentTree.get("deployment")
                                            .get("deploymentSpecifications").get(0)
                                            .get("autoscalingConfiguration").get("scaleDownDetails")
                                            .get("stickiness").get("threshold").asString();
                                    assertThat(size).isEqualToIgnoringCase("PT30M");
                                    assertThat(threshold).isEqualToIgnoringCase("PT5M");
                                }
                            }
                            if (autoscalingConfiguration.scaleUpDetails() != null) {
                                var scaleUpDetails = autoscalingConfiguration.scaleUpDetails();
                                if (scaleUpDetails.stickiness() != null) {
                                    var sw = scaleUpDetails.stickiness();
                                    assertThat(sw.size().toString()).hasToString("PT30M");
                                    assertThat(sw.threshold().toString()).hasToString("PT5M");
                                    var size = deploymentTree.get("deployment")
                                            .get("deploymentSpecifications").get(0)
                                            .get("autoscalingConfiguration").get("scaleUpDetails")
                                            .get("stickiness").get("size").asString();
                                    var threshold = deploymentTree.get("deployment")
                                            .get("deploymentSpecifications").get(0)
                                            .get("autoscalingConfiguration").get("scaleUpDetails")
                                            .get("stickiness").get("threshold").asString();
                                    assertThat(size).isEqualToIgnoringCase("PT30M");
                                    assertThat(threshold).isEqualToIgnoringCase("PT5M");
                                }
                            }
                        }
                    });
        }
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("autoscalingConfigurationArgs")
    void shouldUpdateGpuSpecWithAutoscalingConfig(String configPayload,
                                                  HttpStatus expectedStatus) {
        var deploymentDtoTemplate = readFileAsString(CONFIG_UPDATE_GPU_SPEC_TEMPLATE_TXT);
        var payload = deploymentDtoTemplate.formatted(configPayload);

        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        // Create entries in functions_deployment_v2 table using the following specs:
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update existing function deployment using null deployment specs.
        var requestEntity = RequestEntity
                .patch("/v2/nvcf"
                               + "/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + TEST_GPU_SPEC_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes());

        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (HttpStatus.OK.equals(expectedStatus)) {
            var responseBody = responseEntity.getBody();
            var gpuSpecTree = jsonMapper.readTree(responseBody);
            var gpuSpec = jsonMapper
                    .readValue(responseBody, UpdateGpuSpecificationResponse.class)
                    .gpuSpecification();
            if (gpuSpec != null) {
                var autoscalingConfiguration = gpuSpec.autoscalingConfiguration();
                if (autoscalingConfiguration != null) {
                    if (autoscalingConfiguration.scaleDownDetails() != null) {
                        var scaleDownDetails = autoscalingConfiguration.scaleDownDetails();
                        if (scaleDownDetails.stickiness() != null) {
                            var sw = scaleDownDetails.stickiness();
                            assertThat(sw.size().toString()).hasToString("PT30M");
                            assertThat(sw.threshold().toString()).hasToString("PT5M");
                            var size = gpuSpecTree.get("gpuSpecification")
                                    .get("autoscalingConfiguration").get("scaleDownDetails")
                                    .get("stickiness").get("size").asString();
                            var threshold = gpuSpecTree.get("gpuSpecification")
                                    .get("autoscalingConfiguration").get("scaleDownDetails")
                                    .get("stickiness").get("threshold").asString();
                            assertThat(size).isEqualToIgnoringCase("PT30M");
                            assertThat(threshold).isEqualToIgnoringCase("PT5M");
                        }
                    }
                    if (autoscalingConfiguration.scaleUpDetails() != null) {
                        var scaleUpDetails = autoscalingConfiguration.scaleUpDetails();
                        if (scaleUpDetails.stickiness() != null) {
                            var sw = scaleUpDetails.stickiness();
                            assertThat(sw.size().toString()).hasToString("PT30M");
                            assertThat(sw.threshold().toString()).hasToString("PT5M");
                            var size = gpuSpecTree.get("gpuSpecification")
                                    .get("autoscalingConfiguration").get("scaleUpDetails")
                                    .get("stickiness").get("size").asString();
                            var threshold = gpuSpecTree.get("gpuSpecification")
                                    .get("autoscalingConfiguration").get("scaleUpDetails")
                                    .get("stickiness").get("threshold").asString();
                            assertThat(size).isEqualToIgnoringCase("PT30M");
                            assertThat(threshold).isEqualToIgnoringCase("PT5M");
                        }
                    }
                }
            }
        }
    }

    @Test
    void shouldRemoveAutoscalingConfigWhenPlatformConfigurationPolicy() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create deployment with existing autoscaling config
        var gpuSpecs = Set.of(GpuSpecificationEntity.builder()
                                      .key(GpuSpecificationKey.builder()
                                                   .ncaId(TEST_NCA_ID)
                                                   .deploymentId(TEST_DEPLOYMENT_ID)
                                                   .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                   .build())
                                      .instanceType(T10_INSTANCE_TYPE)
                                      .gpu(T10).maxInstances(5).minInstances(0)
                                      .build());
        var initialConfig = Map.of(TEST_GPU_SPEC_ID, ByteBuffer.wrap(
                toAutoscalingConfigurationProto(createAutoscalingConfigDto()).toByteArray()));
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                gpuSpecs, initialConfig);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // First verify config exists
        var requestBody = UpdateGpuSpecificationRequest.builder()
                .minInstances(1).build();
        var requestEntity = RequestEntity
                .patch("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + TEST_GPU_SPEC_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(
                requestEntity, UpdateGpuSpecificationResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(responseEntity.getBody()).isNotNull();
        assertThat(responseEntity.getBody().gpuSpecification().autoscalingConfiguration())
                .isNotNull();

        // Now send PLATFORM_CONFIGURATION to remove the config
        var removeConfigRequest = UpdateGpuSpecificationRequest.builder()
                .autoscalingConfigurationPolicy(PLATFORM_CONFIGURATION)
                .build();
        var removeRequestEntity = RequestEntity
                .patch("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + TEST_GPU_SPEC_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(removeConfigRequest);

        var removeResponse = testRestTemplate.exchange(
                removeRequestEntity, UpdateGpuSpecificationResponse.class);
        assertThat(removeResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(removeResponse.getBody()).isNotNull();
        // Config should now be null/removed
        assertThat(removeResponse.getBody().gpuSpecification().autoscalingConfiguration()).isNull();
    }
}
