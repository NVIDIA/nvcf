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
package com.nvidia.nvcf.icms.client;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_REGISTRY_CRED;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_REGISTRY_CRED;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_INSTANCE_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_CHART_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_WITH_CANARY_HOST_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.buildHelmValidationPolicyDto;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;
import static org.springframework.http.HttpStatus.NOT_FOUND;

import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonAuthDto;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonDto;
import com.nvidia.nvcf.rest.registry.dto.K8sSecretsDto;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.net.URI;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Instant;
import java.util.Arrays;
import java.util.Base64;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(classes = {NvcfTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "nvcf.global-fqdn-grpc=http://localhost:9090"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class IcmsClientTest {
    @Autowired
    private Clock clock;
    @Autowired
    private JsonMapper jsonMapper;
    @Autowired
    private FunctionMapperService functionMapperService;
    @Autowired
    private FunctionsRepository functionsRepository;
    @Autowired
    private IcmsClient icmsClient;
    @Autowired
    private IcmsAllocatorService icmsAllocatorService;

    @Autowired
    private TestDeploymentService testService;
    @Autowired
    private TestAccountService testAccountService;
    @Autowired
    private TestCommonService testCommonService;
    @Autowired
    private TestQueueService testQueueService;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;
    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;
    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;
    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;
    @Value("${nvcf.sidecars.inference-container}")
    private String inferenceContainer;

    private GpuSpecificationEntity gpuSpecification;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockEssServer.start(essBaseUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @BeforeEach
    void beforeEach() {
        MockIcmsServer.start(9096, jsonMapper);
    }

    @AfterEach
    void reset() {
        MockIcmsServer.stop();
        testCommonService.reset();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockEssServer.stop();
        testQueueService.clearQueues();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @Test
    @SneakyThrows
    void testSendCredentialsForContainerBasedFunction() {
        // Arrange
        when(clock.instant()).thenReturn(Instant.parse("2023-11-23T06:00:00Z"));

        var function = testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                            TEST_NCA_ID, TEST_FUNCTION_NAME);
        var functionDeploymentEntity = testService.createDeploymentEntity(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
        gpuSpecification = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(40)
                .minInstances(1)
                .maxRequestConcurrency(9)
                .build();

        // Act
        var deploymentId = functionDeploymentEntity.getDeploymentId();
        icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpecification, 1);

        // Assert
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateEnvironmentVariables(formUrlEncodedBody, false,
                                     false,
                                     TEST_NGC_CONTAINER_IMAGE.toString(),
                                     null,
                                     List.of("stg.nvcr.io"));
        validateFunctionName(formUrlEncodedBody, TEST_FUNCTION_NAME);
    }

    @Test
    @SneakyThrows
    void testSendCredentialsForHelmBasedFunction() {
        // Arrange
        when(clock.instant()).thenReturn(Instant.parse("2023-11-23T06:00:00Z"));

        var function =
                testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                               TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                               FunctionStatus.ACTIVE);
        var functionDeploymentEntity = testService.createDeploymentEntity(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
        var policyDto = buildHelmValidationPolicyDto();
        gpuSpecification = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(40)
                .minInstances(1)
                .maxRequestConcurrency(9)
                .helmValidationPolicy(jsonMapper.writeValueAsString(policyDto))
                .build();

        // Act
        var deploymentId = functionDeploymentEntity.getDeploymentId();
        icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpecification, 1);

        // Assert
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateEnvironmentVariables(formUrlEncodedBody, true,
                                     false,
                                     inferenceContainer,
                                     "helm.stg.ngc.nvidia.com",
                                     List.of("stg.nvcr.io", "canary.nvcr.io"));
        validateFunctionName(formUrlEncodedBody, TEST_FUNCTION_NAME);
        validateHelmPolicy(formUrlEncodedBody, policyDto);
    }

    @Test
    @SneakyThrows
    void testSendLlmImagesForLlmFunction() {
        // Arrange
        when(clock.instant()).thenReturn(Instant.parse("2023-11-23T06:00:00Z"));

        var function = testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                            TEST_NCA_ID, TEST_FUNCTION_NAME);
        var expectedModels = List.of(
                FunctionModelDto.builder()
                        .name("meta/llama-3.1-8b-instruct")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                                .uris(List.of("/v1/chat/completions"))
                                .tokenRateLimit("1-S")
                                .tokenizer("meta-llama-tokenizer")
                                .routingMethod("sticky")
                                .build())
                        .build());
        function.setFunctionType(FunctionType.LLM);
        function.setModelSpecs(functionMapperService.toModelSpecs(expectedModels));
        functionsRepository.save(function);
        var functionDeploymentEntity = testService.createDeploymentEntity(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
        gpuSpecification = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(40)
                .minInstances(1)
                .maxRequestConcurrency(9)
                .build();

        // Act
        var deploymentId = functionDeploymentEntity.getDeploymentId();
        icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpecification, 1);

        // Assert
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateEnvironmentVariables(formUrlEncodedBody, false,
                                     true,
                                     TEST_NGC_CONTAINER_IMAGE.toString(),
                                     null,
                                     List.of("stg.nvcr.io"));
        assertThat(new String(Base64.getDecoder().decode(URLDecoder.decode(
                parseRequestParams(formUrlEncodedBody).get("LaunchSpecification.Models"),
                StandardCharsets.UTF_8)), StandardCharsets.UTF_8)).isEqualTo(
                jsonMapper.writeValueAsString(
                expectedModels));
        validateFunctionName(formUrlEncodedBody, TEST_FUNCTION_NAME);
    }


    @Test
    @SneakyThrows
    void testSendCredentialsForContainerBasedFunctionWithCanaryRegistries() {
        // Arrange
        when(clock.instant()).thenReturn(Instant.parse("2023-11-23T06:00:00Z"));
        var function = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .modelSpecs(functionMapperService.toModelSpecs(List.of(
                        FunctionModelDto.builder()
                                .name("model-1")
                                .uri(URI.create(TEST_MODEL_URL_WITH_CANARY_HOST))
                                .build()
                )))
                .resources(Set.of(
                        ResourceUdt.builder()
                                .name("resource-1")
                                .url(TEST_RESOURCE_URL_WITH_CANARY_HOST_1)
                                .build()
                                 ))
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(function);
        var functionDeploymentEntity = testService.createDeploymentEntity(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
        var policyDto = buildHelmValidationPolicyDto();
        gpuSpecification = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(40)
                .minInstances(1)
                .maxRequestConcurrency(9)
                .helmValidationPolicy(jsonMapper.writeValueAsString(policyDto))
                .build();

        // Act
        var deploymentId = functionDeploymentEntity.getDeploymentId();
        icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpecification, 1);

        // Assert
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateEnvironmentVariables(formUrlEncodedBody, false,
                                     false,
                                     TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST.toString(),
                                     "helm.canary.ngc.nvidia.com",
                                     List.of("canary.nvcr.io"));
        validateFunctionName(formUrlEncodedBody, TEST_FUNCTION_NAME);
        validateHelmPolicy(formUrlEncodedBody, policyDto);
    }

    @Test
    @SneakyThrows
    void testSendCredentialsFromEssForHelmBasedFunctionWithCanaryRegistries() {
        // Arrange
        when(clock.instant()).thenReturn(Instant.parse("2023-11-23T06:00:00Z"));
        var function = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .helmChart(TEST_NGC_HELM_CHART_WITH_CANARY_HOST.toString())
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .modelSpecs(functionMapperService.toModelSpecs(List.of(
                        FunctionModelDto.builder()
                                .name("model-1")
                                .uri(URI.create(TEST_MODEL_URL_WITH_CANARY_HOST))
                                .build()
                )))
                .resources(Set.of(
                        ResourceUdt.builder()
                                .name("resource-1")
                                .url(TEST_RESOURCE_URL_WITH_CANARY_HOST_1)
                                .build()
                                 ))
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(function);
        var functionDeploymentEntity = testService.createDeploymentEntity(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
        var policyDto = buildHelmValidationPolicyDto();
        gpuSpecification = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(40)
                .minInstances(1)
                .maxRequestConcurrency(9)
                .helmValidationPolicy(jsonMapper.writeValueAsString(policyDto))
                .build();

        // Act
        var deploymentId = functionDeploymentEntity.getDeploymentId();
        icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpecification, 1);

        // Assert
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateEnvironmentVariables(formUrlEncodedBody, true,
                                     false,
                                     inferenceContainer,
                                     "helm.canary.ngc.nvidia.com",
                                     List.of("stg.nvcr.io", "canary.nvcr.io"));
        validateFunctionName(formUrlEncodedBody, TEST_FUNCTION_NAME);
        validateHelmPolicy(formUrlEncodedBody, policyDto);
    }

    @Test
    void shouldFetchInstances() {
        icmsClient.getInstancesByDeploymentId(TEST_NCA_ID, TEST_DEPLOYMENT_ID);
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String url = allServeEvents.getFirst().getRequest().getUrl();
        assertThat(url).isEqualTo("/v1/si/accounts/" + TEST_NCA_ID +
                                          "/workloads/" + TEST_DEPLOYMENT_ID +
                                          "/instances?IncludeTerminated=false" +
                                          "&UseConciseName=true&ExpiredAckedInstances=false");

        icmsClient.getInstancesByDeploymentId(TEST_NCA_ID, TEST_DEPLOYMENT_ID, true, true);
        allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        url = allServeEvents.getFirst().getRequest().getUrl();
        assertThat(url).isEqualTo("/v1/si/accounts/" + TEST_NCA_ID +
                                          "/workloads/" + TEST_DEPLOYMENT_ID +
                                          "/instances?IncludeTerminated=true&UseConciseName=true" +
                                          "&ExpiredAckedInstances=true");
    }

    @Test
    void shouldDeleteInstanceUsingAccountEndpoint() {
        var instanceId = TEST_INSTANCE_ID;

        icmsClient.deleteInstance(TEST_NCA_ID, instanceId);

        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        var request = allServeEvents.getFirst().getRequest();
        assertThat(request.getMethod().getName()).isEqualTo("DELETE");
        assertThat(request.getUrl()).isEqualTo(
                "/v1/si/accounts/" + TEST_NCA_ID + "/instances/" + instanceId);
    }

    @Test
    void shouldDeleteInstancesByDeploymentIdUsingWorkloadEndpoint() {
        icmsClient.deleteInstancesByDeploymentId(TEST_NCA_ID, TEST_DEPLOYMENT_ID);

        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        var request = allServeEvents.getFirst().getRequest();
        assertThat(request.getMethod().getName()).isEqualTo("DELETE");
        assertThat(request.getUrl()).isEqualTo(
                "/v1/si/accounts/" + TEST_NCA_ID + "/workloads/" + TEST_DEPLOYMENT_ID);
    }

    @Test
    void shouldReturnEmptyInstancesWhenDeploymentInstancesNotFound() {
        MockIcmsServer.getMockIcmsServer().stubFor(
                get(urlPathMatching("/v1/si/accounts/(.+)?/workloads/(.+)?/instances"))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(NOT_FOUND.value())));

        var instances = icmsClient.getInstancesByDeploymentId(TEST_NCA_ID, TEST_DEPLOYMENT_ID);

        assertThat(instances).isEmpty();
    }

    private void validateFunctionName(String formUrlEncodedBody, String expectedFunctionName) {
        var params = parseRequestParams(formUrlEncodedBody);
        assertThat(params.get("FunctionDetails.FunctionName")).isEqualTo(expectedFunctionName);
    }

    private Map<String, String> parseRequestParams(String formUrlEncodedBody) {
        return Arrays.stream(formUrlEncodedBody.split("&"))
                .collect(Collectors.toMap(
                        r -> r.split("=")[0],
                        r -> r.substring(r.indexOf("=") + 1)
                                         ));
    }

    // NVCF_WORKER_TOKEN and ARTIFACTS_URL are generated dynamically; LaunchSpecification
    // .Environment cannot be checked with regex directly.
    @SneakyThrows
    private Map<String, String> validateEnvironmentVariables(String formUrlEncodedBody,
                                                             boolean isHelmBasedFunction,
                                                             boolean expectLlmSidecarImages,
                                                             String inferenceContainer,
                                                             String helmChartRegistry,
                                                             List<String> containerRegistries) {
        var params = parseRequestParams(formUrlEncodedBody);
        var envRaw = new String(
                Base64.getDecoder().decode(
                        URLDecoder.decode(params.get("LaunchSpecification.Environment"),
                                          StandardCharsets.UTF_8)
                                          )
        );
        var env = Arrays.stream(envRaw.split("\n"))
                .collect(Collectors.toMap(
                        x -> x.split("=")[0],
                        x -> x.substring(x.indexOf("=") + 1)
                                         ));

        var base64EncodedContainerCredentials = Base64.getEncoder().encodeToString(
                ("$oauthtoken:" + TEST_CONTAINER_REGISTRY_CRED).getBytes(
                        StandardCharsets.UTF_8));
        var base64EncodedHelmCredentials = Base64.getEncoder().encodeToString(
                ("$oauthtoken:" + TEST_HELM_REGISTRY_CRED).getBytes(
                        StandardCharsets.UTF_8));
        var base64EncodedSidecarCredentials = Base64.getEncoder().encodeToString(
                "$oauthtoken:nvapi-stg-dummy-sidecar-secret-for-integration-tests".getBytes(
                        StandardCharsets.UTF_8)
                                                                                );

        String expectedHelmRegistriesCredentialsRaw;
        if (!isHelmBasedFunction) {
            assertThat(env.get("INFERENCE_CONTAINER")).isEqualTo(inferenceContainer);
            assertThat(env.get("INFERENCE_CONTAINER_ARGS")).isEqualTo(TEST_CONTAINER_ARGS);
            assertThat(env.get("HELM_CHART_INFERENCE_SERVICE_NAME")).isEmpty();
            expectedHelmRegistriesCredentialsRaw = """
                    {
                      "k8sSecrets": []
                    }
                    """;
        } else {
            assertThat(env.get("INFERENCE_CONTAINER")).isEqualTo(inferenceContainer);
            assertThat(env.get("INFERENCE_CONTAINER_ARGS")).isEmpty();
            assertThat(env.get("HELM_CHART_INFERENCE_SERVICE_NAME")).isEqualTo("ENTRYPOINT");
            expectedHelmRegistriesCredentialsRaw = """
                    {
                      "k8sSecrets": [
                        {
                          "auths": {
                            "%s": {
                              "auth": "%s"
                            }
                          }
                        }
                      ]
                    }
                    """.formatted(helmChartRegistry, base64EncodedHelmCredentials);
        }

        assertThat(env.get("NVCF_FQDN")).isEqualTo("http://localhost:0");
        assertThat(env.get("NVCF_FQDN_GRPC")).isEqualTo("http://localhost:9090");
        assertThat(env.get("NVCF_FQDN_NATS")).isEqualTo(
                "tls://connect.pnats.stg.nvcf.nvidia.com:4222");
        assertThat(env.get("INFERENCE_CONTAINER_ENV")).isEqualTo("W10=");
        assertThat(env.get("INFERENCE_HEALTH_ENDPOINT")).isEqualTo(TEST_HEALTH_ENDPOINT);
        assertThat(env.get("INFERENCE_HEALTH_PROTOCOL")).isEqualTo("HTTP");
        assertThat(env.get("INFERENCE_HEALTH_TIMEOUT")).isEqualTo(HEALTH_TIMEOUT.toString());
        assertThat(env.get("INFERENCE_HEALTH_PORT")).isEqualTo(String.valueOf(TEST_INFERENCE_PORT));
        assertThat(env.get("INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE")).isEqualTo(
                String.valueOf(EXPECTED_STATUS_CODE));
        assertThat(env.get("INFERENCE_URL")).isEqualTo(TEST_INFERENCE_URL.toString());
        assertThat(env.get("INFERENCE_PORT")).isEqualTo("8000");
        assertThat(env.get("INFERENCE_PROTOCOL")).isEqualTo("REST");
        assertThat(env.get("OTEL_EXPORTER_OTLP_ENDPOINT")).isEqualTo("https://dummy:8282");
        assertThat(env.get("TRACING_ACCESS_TOKEN")).isEqualTo("dummy-lightstep-access-token");
        assertThat(env.get("INIT_CONTAINER")).isEqualTo(
                "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:0.7.0");
        assertThat(env.get("OTEL_CONTAINER")).isEqualTo(
                "docker.io/otel/opentelemetry-collector:0.74.0");
        assertThat(env.get("UTILS_CONTAINER")).isEqualTo(
                "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_utils:2.2.1");
        assertThat(env.get("NCA_ID")).isEqualTo(TEST_NCA_ID);
        assertThat(env.get("FUNCTION_NAME")).isEqualTo(TEST_FUNCTION_NAME);
        var tags = new HashSet<>(Arrays.asList(env.get("FUNCTION_TAGS").split(",")));
        assertThat(tags).containsAll(TEST_TAGS);
        assertThat(env.get("MAX_REQUEST_CONCURRENCY")).isEqualTo("9");
        assertThat(env.get("SECRETS_ASSERTION_TOKEN")).isEqualTo("");
        assertThat(env.get("ESS_AGENT_CONTAINER")).isEqualTo(
                "stg.nvcr.io/nv-cf/nvcf-core/ess-agent:0.0.4");
        assertThat(params).containsKey("LaunchSpecification.Models");
        if (expectLlmSidecarImages) {
            assertThat(env.get("LLM_CREDENTIAL_MANAGER_IMAGE")).isEqualTo(
                    "stg.nvcr.io/nv-cf/nvcf-core/llm-credential-manager:0.1.0");
            assertThat(env.get("LLM_ROUTER_CLIENT_IMAGE")).isEqualTo(
                    "stg.nvcr.io/nv-cf/nvcf-core/llm-worker:0.1.0");
            assertThat(env.get("LLM_REQUEST_ROUTER_ADDRESS")).isEqualTo(
                    "llm-request-router.example.com:50071");
            assertThat(env).doesNotContainKey("STARGATE_ADDRESS");
        } else {
            assertThat(env).doesNotContainKeys("LLM_CREDENTIAL_MANAGER_IMAGE",
                                               "LLM_ROUTER_CLIENT_IMAGE",
                                               "LLM_REQUEST_ROUTER_ADDRESS",
                                               "STARGATE_ADDRESS");
        }
        assertThat(env.get("FUNCTION_ID")).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(env.get("FUNCTION_VERSION_ID")).isEqualTo(TEST_VERSION_ID_1.toString());
        assertThat(env)
                .containsKey("CONTAINER_REGISTRIES_CREDENTIALS")
                .containsKey("HELM_REGISTRIES_CREDENTIALS")
                .containsKey("SIDECAR_REGISTRY_CREDENTIAL");

        var helmRegistriesCredentials = jsonMapper.readValue(
                Base64.getDecoder().decode(env.get("HELM_REGISTRIES_CREDENTIALS")),
                K8sSecretsDto.class);
        var expectedHelmRegistriesCredentials =
                jsonMapper.readValue(expectedHelmRegistriesCredentialsRaw, K8sSecretsDto.class);
        assertThat(helmRegistriesCredentials).isEqualTo(expectedHelmRegistriesCredentials);
        var containerRegistriesCredentials = jsonMapper.readValue(
                Base64.getDecoder().decode(env.get("CONTAINER_REGISTRIES_CREDENTIALS")),
                K8sSecretsDto.class);

        var expectedContainerRegistriesCredentials = K8sSecretsDto.builder()
                .k8sSecrets(containerRegistries.stream()
                                    .map(containerRegistry -> DockerConfigJsonDto.builder()
                                            .auths(Map.of(containerRegistry,
                                                    DockerConfigJsonAuthDto.builder()
                                                            .auth(base64EncodedContainerCredentials)
                                                            .build()))
                                            .build()).toList()).build();
        assertThat(containerRegistriesCredentials).isEqualTo(
                expectedContainerRegistriesCredentials);
        var sidecarRegistryCredential = jsonMapper.readValue(
                Base64.getDecoder().decode(env.get("SIDECAR_REGISTRY_CREDENTIAL")),
                K8sSecretsDto.class);
        var expectedSidecarRegistryCredentialRaw = """
                {
                  "auths": {
                    "stg.nvcr.io": {
                      "auth": "%s"
                    }
                  }
                }
                """.formatted(base64EncodedSidecarCredentials);
        var expectedSidecarRegistryCredential =
                jsonMapper.readValue(expectedSidecarRegistryCredentialRaw, K8sSecretsDto.class);
        assertThat(sidecarRegistryCredential).isEqualTo(expectedSidecarRegistryCredential);
        return env;
    }

    @SneakyThrows
    private void validateHelmPolicy(String formUrlEncodedBody, HelmValidationPolicyDto policyDto) {
        var params = Arrays.stream(formUrlEncodedBody.split("&"))
                .collect(Collectors.toMap(
                        r -> r.split("=")[0],
                        r -> r.substring(r.indexOf("=") + 1)
                ));
        var policyRaw = new String(
                Base64.getDecoder().decode(
                        URLDecoder.decode(params.get("LaunchSpecification.HelmValidationPolicy"),
                                          StandardCharsets.UTF_8)
                )
        );
        assertThat(policyRaw).isNotBlank();
        var policyEncoded = jsonMapper.readValue(
                policyRaw, HelmValidationPolicyDto.class);
        assertThat(policyEncoded).isEqualTo(policyDto);
    }
}
