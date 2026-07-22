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
package com.nvidia.nvct.service.icms;

import static com.nvidia.nvct.util.TestConstants.BASE64_CONTAINER_REGISTRY_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_ACR_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_ARTIFACTORY_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_DOCKER_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_ECR_PRIVATE_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_ECR_PUBLIC_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_HARBOR_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_ENCODED_VOLCENGINE_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_HELM_REGISTRY_CRED;
import static com.nvidia.nvct.util.TestConstants.BASE64_SIDECAR_REGISTRY_CRED;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_TEAM_NAME;
import static com.nvidia.nvct.util.TestUtil.buildHelmValidationPolicyDto;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.HelmValidationPolicyDto;
import com.nvidia.nvct.service.registry.RegistryArtifactService;
import com.nvidia.nvct.service.registry.dto.K8sSecretsDto;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.TestUtil;
import java.net.URL;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.logging.log4j.util.Strings;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class IcmsServiceTest {

    @Autowired
    private IcmsService icmsService;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private IcmsClient icmsClient;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TaskService taskService;

    @Autowired
    private RegistryArtifactService artifactService;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvct.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${spring.security.oauth2.client.registration.notary.client-id}")
    private String notaryClientId;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void afterEach() {
        artifactService.invalidateCache();
        testTaskService.clearAll();
    }

    @Test
    void shouldScheduleInstanceForContainerBasedTask() {
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        taskService.saveTask(task1);

        // Act
        var requestId = icmsService.scheduleInstance(task1);

        // Assert
        assertThat(requestId).isNotNull();
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateTaskInstancePayload(formUrlEncodedBody, task1);

        taskService.deleteTask(task1);
    }

    @Test
    void shouldScheduleInstanceForHelmBasedTask() {
        var task1 =
                TestUtil.createHelmBasedTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                                   jsonMapper);
        taskService.saveTask(task1);

        // Act
        var requestId = icmsService.scheduleInstance(task1);

        // Assert
        assertThat(requestId).isNotNull();
        List<ServeEvent> allServeEvents = MockIcmsServer.getMockIcmsServer().getAllServeEvents();
        String formUrlEncodedBody = allServeEvents.getFirst().getRequest().getBodyAsString();
        validateTaskInstancePayload(formUrlEncodedBody, task1);

        taskService.deleteTask(task1);
    }

    @Test
    void shouldDeleteInstances() {
        icmsClient.deleteInstances(List.of("ids"));
    }

    @Test
    void shouldTolerateTerminateWhenIcmsWorkloadNotFound() {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockIcmsServer.stubTerminateInstanceNotFound();

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);

        var result = icmsService.terminateInstanceByTaskId(TEST_NCA_ID, TEST_TASK_ID_1);

        assertThat(result).isTrue();

        taskService.deleteTask(TEST_TASK_ID_1);
    }

    @SneakyThrows
    private void validateTaskInstancePayload(String formUrlEncodedBody, TaskEntity task) {
        Map<String, Object> paramMap = Arrays.stream(formUrlEncodedBody.split("&"))
                .map(s -> s.split("=", 2))
                .collect(Collectors.toMap(
                        key -> decode(key[0]),
                        value -> decode(value.length > 1 ? value[1] : ""),
                        (existing, incoming) -> {
                            if (existing instanceof String) {
                                List<String> list = new ArrayList<>();
                                list.add((String) existing);
                                list.add((String) incoming);
                                return list;
                            } else {
                                ((List<String>) existing).add((String) incoming);
                                return existing;
                            }
                        }));

        assertThat(paramMap)
                .containsEntry("TaskDetails.TaskName", task.getName())
                .containsEntry("LaunchSpecification.NcaId", TEST_NCA_ID)
                .containsEntry("LaunchSpecification.TerminationGracePeriodDuration", "PT1H")
                .containsEntry("LaunchSpecification.CacheArtifacts", "true")
                .containsEntry("LaunchSpecification.GpuSpecificationId", TEST_TASK_ID_1.toString())
                .containsEntry("LaunchSpecification.Gpu", "T10")
                .containsEntry("LaunchSpecification.Backend", "GFN")
                .containsEntry("LaunchSpecification.InstanceType", "g6.full")
                .containsEntry("TaskDetails.AccountName", "test-nca-id-name")
                .containsEntry("InstanceCount", "1")
                .containsEntry("LaunchSpecification.CacheHandle",
                               "da56d7b9f5b145646f32b571d19ae4bf")
                .containsEntry("LaunchSpecification.CacheSize", "232476667482")
                .containsEntry("TaskDetails.OwnerNcaId", TEST_NCA_ID)
                .containsEntry("TaskDetails.TaskId", TEST_TASK_ID_1.toString())
                .containsEntry("LaunchSpecification.MaxRuntimeDuration", "PT7H")
                .containsEntry("LaunchSpecification.MaxQueuedDuration", "PT24H")
                .containsEntry("LaunchSpecification.ResultHandlingStrategy",
                               ResultHandlingStrategy.UPLOAD.name())
                .containsEntry("LaunchSpecification.Telemetries", "")
                .containsEntry("LaunchSpecification.DeploymentId", TEST_TASK_ID_1.toString());

        // validate helm validation policy
        var policyRaw = (String) paramMap.get("LaunchSpecification.HelmValidationPolicy");
        assertThat(policyRaw).isNotBlank();
        var policyDecoded =
                new String(Base64.getDecoder().decode(policyRaw), StandardCharsets.UTF_8);
        var policy = jsonMapper.readValue(policyDecoded, HelmValidationPolicyDto.class);
        assertThat(policy).isEqualTo(buildHelmValidationPolicyDto());

        // Validate environment variables
        String environment = (String) paramMap.get("LaunchSpecification.Environment");
        assertThat(environment).isNotNull();
        Map<String, String> envVars = decodeEnvironmentVariables(environment);

        assertThat(envVars)
                .containsEntry("NCA_ID", TEST_NCA_ID)
                .containsEntry("ACCOUNT_NAME", "test-nca-id-name")
                .containsEntry("TASK_ID", TEST_TASK_ID_1.toString())
                .containsEntry("TASK_NAME", task.getName())
                .containsEntry("TERMINATION_GRACE_PERIOD", "PT1H")
                .containsEntry("TASK_SECRETS_PRESENT", "false")
                .containsEntry("NVCT_FQDN", "http://localhost:0")
                .containsEntry("NVCT_FQDN_GRPC", "http://localhost:9090")
                .containsEntry("INIT_CONTAINER",
                               "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:0.7.0")
                .containsEntry("OTEL_CONTAINER", "docker.io/otel/opentelemetry-collector:0.74.0")
                .containsEntry("UTILS_CONTAINER",
                               "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_utils:2.2.1")
                .containsEntry("ESS_AGENT_CONTAINER", "stg.nvcr.io/nv-cf/nvcf-core/ess-agent:0.0.4")
                .containsEntry("OTEL_EXPORTER_OTLP_ENDPOINT", "https://dummy:8282")
                .containsEntry("TASK_CONTAINER_ENV",
                               "W3sia2V5IjoiS0VZXzEiLCJ2YWx1ZSI6IlZBTFVFXzEifSx7ImtleSI6IktFWV8yIiwidmFsdWUiOiJWQUxVRV8yIn0seyJrZXkiOiJLRVlfMyIsInZhbHVlIjoiVkFMVUVfMyJ9XQ==")
                .containsEntry("RESULTS_LOCATION",
                               TEST_VALID_ORG_NAME + "/" + TEST_VALID_TEAM_NAME +
                                       "/test-model-name")
                .containsEntry("TRACING_ACCESS_TOKEN", "dummy-worker-lightstep-access-token")
                .containsEntry("BYOO_OTEL_COLLECTOR_CONTAINER",
                               "stg.nvcr.io/nv-cf/nvcf-core/byoo-otel-collector:1.2.3")
                .containsEntry("SECRETS_ASSERTION_TOKEN", "")
                .containsKey("NVCT_WORKER_TOKEN")
                .containsKey("CONTAINER_REGISTRIES_CREDENTIALS")
                .containsKey("HELM_REGISTRIES_CREDENTIALS")
                .containsKey("SIDECAR_REGISTRY_CREDENTIAL");

        var containerRegistriesCredentials = jsonMapper.readValue(
                Base64.getDecoder().decode(envVars.get("CONTAINER_REGISTRIES_CREDENTIALS")),
                K8sSecretsDto.class);

        var helmRegistriesCredentials = jsonMapper.readValue(
                Base64.getDecoder().decode(envVars.get("HELM_REGISTRIES_CREDENTIALS")),
                K8sSecretsDto.class);

        if (Strings.isBlank(task.getHelmChart())) {
            var expectedContainerRegistriesCredentialsRaw = """
                {
                  "k8sSecrets": [
                    {
                      "auths": {
                        "stg.nvcr.io": {
                          "auth": "%s"
                        }
                      }
                    }
                  ]
                }
                """.formatted(BASE64_CONTAINER_REGISTRY_CRED);
            var expectedContainerRegistriesCredentials =
                    jsonMapper.readValue(expectedContainerRegistriesCredentialsRaw,
                                         K8sSecretsDto.class);
            assertThat(containerRegistriesCredentials).isEqualTo(
                    expectedContainerRegistriesCredentials);
            assertThat(paramMap)
                    .containsEntry("LaunchSpecification.ContainerImage",
                                   TEST_CONTAINER_IMAGE.toString())
                    .containsEntry("LaunchSpecification.HelmChart", "");
            assertThat(envVars)
                    .containsEntry("TASK_CONTAINER", TEST_CONTAINER_IMAGE.toString())
                    .containsEntry("TASK_CONTAINER_ARGS", TEST_CONTAINER_ARGS);
            String expectedHelmRegistriesCredentialsRaw = """
                    {
                      "k8sSecrets": []
                    }
                    """;
            var expectedHelmRegistriesCredentials =
                    jsonMapper.readValue(expectedHelmRegistriesCredentialsRaw,
                                         K8sSecretsDto.class);
            assertThat(helmRegistriesCredentials).isEqualTo(expectedHelmRegistriesCredentials);
        } else {
            var expectedContainerRegistriesCredentialsRaw = """
                    {
                      "k8sSecrets": [
                        {
                          "auths": {
                            "docker.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "779846807323.dkr.ecr.us-west-2.amazonaws.com": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "public.ecr.aws": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "test-volcengine-registry-cn-beijing.cr.volces.com": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "test1-bmfvajfxgfcrhba5.azurecr.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "demo.goharbor.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "artifactorytest12345.jfrog.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "stg.nvcr.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "canary.nvcr.io": {
                              "auth": "%s"
                            }
                          }
                        },
                        {
                          "auths": {
                            "localhost-ngc": {
                              "auth": "%s"
                            }
                          }
                        }
                      ]
                    }
                    """.formatted(BASE64_ENCODED_DOCKER_CRED,
                                  BASE64_ENCODED_ECR_PRIVATE_CRED,
                                  BASE64_ENCODED_ECR_PUBLIC_CRED,
                                  BASE64_ENCODED_VOLCENGINE_CRED,
                                  BASE64_ENCODED_ACR_CRED,
                                  BASE64_ENCODED_HARBOR_CRED,
                                  BASE64_ENCODED_ARTIFACTORY_CRED,
                                  BASE64_CONTAINER_REGISTRY_CRED,
                                  BASE64_CONTAINER_REGISTRY_CRED,
                                  BASE64_CONTAINER_REGISTRY_CRED);
            var expectedContainerRegistriesCredentials =
                    jsonMapper.readValue(expectedContainerRegistriesCredentialsRaw,
                                         K8sSecretsDto.class);
            assertThat(containerRegistriesCredentials).isEqualTo(
                    expectedContainerRegistriesCredentials);
            assertThat(paramMap)
                    .containsEntry("LaunchSpecification.ContainerImage", "")
                    .containsEntry("LaunchSpecification.HelmChart", TEST_HELM_CHART.toString());
            assertThat(envVars)
                    .containsEntry("TASK_CONTAINER", "")
                    .containsEntry("TASK_CONTAINER_ARGS", "");
            var expectedHelmRegistriesCredentialsRaw = """
                    {
                      "k8sSecrets": [
                        {
                          "auths": {
                            "helm.stg.ngc.nvidia.com": {
                              "auth": "%s"
                            }
                          }
                        }
                      ]
                    }
                    """.formatted(BASE64_HELM_REGISTRY_CRED);
            var expectedHelmRegistriesCredentials =
                    jsonMapper.readValue(expectedHelmRegistriesCredentialsRaw,
                                         K8sSecretsDto.class);
            assertThat(helmRegistriesCredentials).isEqualTo(expectedHelmRegistriesCredentials);

            var sidecarRegistryCredential = jsonMapper.readValue(
                    Base64.getDecoder().decode(envVars.get("SIDECAR_REGISTRY_CREDENTIAL")),
                    K8sSecretsDto.class);
            var expectedSidecarRegistryCredentialRaw = """
                    {
                      "auths": {
                        "stg.nvcr.io": {
                          "auth": "%s"
                        }
                      }
                    }
                    """.formatted(BASE64_SIDECAR_REGISTRY_CRED);
            var expectedSidecarRegistryCredential =
                    jsonMapper.readValue(expectedSidecarRegistryCredentialRaw,
                                         K8sSecretsDto.class);
            assertThat(sidecarRegistryCredential).isEqualTo(expectedSidecarRegistryCredential);
        }
    }

    private static String decode(String s) {
        return URLDecoder.decode(s, StandardCharsets.UTF_8);
    }

    private static Map<String, String> decodeEnvironmentVariables(String base64EncodedEnv) {
        String decodedEnv =
                new String(Base64.getDecoder().decode(base64EncodedEnv), StandardCharsets.UTF_8);
        return Arrays.stream(decodedEnv.split("\n"))
                .filter(line -> line.contains("="))
                .map(line -> line.split("=", 2))
                .collect(Collectors.toMap(
                        keyValue -> keyValue[0],
                        keyValue -> keyValue.length > 1 ? keyValue[1] : ""));
    }
}
