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
package com.nvidia.nvcf.util;

import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_ECR_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_ECR_PUBLIC_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_ECR_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_VOLCENGINE_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_ORG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_HELM_CHART_TAG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_IMAGE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_IMAGE_TAG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_TEAM_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_NOT_EXIST_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_VERSION;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILE_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_SMALL_SIZE_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
import static java.nio.charset.StandardCharsets.UTF_8;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.function.management.dto.ArtifactDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import io.grpc.Metadata;
import io.grpc.Metadata.Key;
import java.net.URI;
import java.time.Duration;
import java.util.Base64;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import tools.jackson.databind.node.StringNode;

public class TestConstants {

    public static final String TEST_ADMIN_SUBJECT = "test-admin-id";

    public static final String TEST_CLIENT_SUBJECT = "test-client-id";
    public static final String TEST_ACCOUNT_NAME = "test-account";
    public static final String TEST_NCA_ID = "test-nca-id";
    public static final String TEST_OWNER_ID = "test-owner-id";
    public static final String TEST_OWNER_ID_2 = "test-owner-id-2";
    public static final String TEST_CLIENT_ID = TEST_CLIENT_SUBJECT;
    public static final String TEST_CLIENT_SUBJECT_2 = "test-client-id-2";
    public static final String TEST_CLIENT_3 = "test-client-id-3";
    public static final String TEST_ACCOUNT_NAME_2 = "test-account-2";
    public static final String TEST_NCA_ID_2 = "test-nca-id-2";
    public static final String TEST_ACCOUNT_NAME_3 = "test-account-3";
    public static final String TEST_NCA_ID_3 = "test-nca-id-3";
    public static final String TEST_CLIENT_ID_2 = TEST_CLIENT_SUBJECT_2;

    public static final String TEST_CONTAINER_REGISTRY_CRED =
            "nvapi-stg-test-container-registry-cred";
    public static final String TEST_HELM_REGISTRY_CRED =
            "nvapi-stg-test-helm-registry-cred";
    public static final String TEST_MODEL_REGISTRY_CRED =
            "nvapi-stg-test-model-registry-cred";
    public static final String TEST_RESOURCE_REGISTRY_CRED =
            "nvapi-stg-test-resource-registry-cred";
    public static final String TEST_SIDECAR_CRED =
            "nvapi-stg-dummy-sidecar-secret-for-integration-tests";
    public static final String TEST_SIDECAR_REGISTRY_CRED_FOR_NVCF_PLATFORM =
            "nvcf-platform-sidecar-registry-cred";

    public static final String BASE64_CONTAINER_REGISTRY_CRED =
            Base64.getEncoder()
                    .encodeToString("$oauthtoken:nvapi-stg-test-container-registry-cred"
                                            .getBytes(UTF_8));
    public static final String BASE64_HELM_REGISTRY_CRED =
            Base64.getEncoder()
                    .encodeToString("$oauthtoken:nvapi-stg-test-helm-registry-cred"
                                            .getBytes(UTF_8));
    public static final String BASE64_MODEL_REGISTRY_CRED =
            Base64.getEncoder()
                    .encodeToString("$oauthtoken:nvapi-stg-test-model-registry-cred"
                                            .getBytes(UTF_8));
    public static final String BASE64_RESOURCE_REGISTRY_CRED =
            Base64.getEncoder()
                    .encodeToString("$oauthtoken:nvapi-stg-test-resource-registry-cred"
                                            .getBytes(UTF_8));
    public static final String BASE64_SIDECAR_REGISTRY_CRED =
            Base64.getEncoder()
                    .encodeToString(
                            "$oauthtoken:nvapi-stg-dummy-sidecar-secret-for-integration-tests".getBytes(
                                    UTF_8));

    public static final String BASE64_SIDECAR_REGISTRY_CRED_FOR_NVCF_PLATFORM =
            Base64.getEncoder().encodeToString("$oauthtoken:nvcf-platform-sidecar-registry-cred"
                                                       .getBytes(UTF_8));


    public static final String TEST_HEALTH_ENDPOINT = "/v2/test/health/endpoint/ready";
    public static final URI TEST_HEALTH_URI = URI.create(TEST_HEALTH_ENDPOINT);
    public static final HealthDto TEST_HEALTH_DTO = HealthDto
            .builder()
            .protocol(ProtocolEnum.HTTP)
            .uri(TEST_HEALTH_URI)
            .port(7777)
            .expectedStatusCode(200)
            .timeout(Duration.ofSeconds(10))
            .build();
    public static final String TEST_REGION = "test-region";

    public static final String TEST_AUTHORIZED_CLIENT_ID_1 = "test-authorized-client-id-1";
    public static final String TEST_AUTHORIZED_NCA_ID_1 = "test-authorized-nca-id-1";
    public static final String TEST_AUTHORIZED_CLIENT_ID_2 = "test-authorized-client-id-2";
    public static final String TEST_AUTHORIZED_NCA_ID_2 = "test-authorized-nca-id-2";
    public static final String TEST_AUTHORIZED_CLIENT_ID_3 = "test-authorized-client-id-3";
    public static final String TEST_AUTHORIZED_NCA_ID_3 = "test-authorized-nca-id-3";
    public static final String TEST_AUTHORIZED_CLIENT_ID_4 = "test-authorized-client-id-4";
    public static final String TEST_AUTHORIZED_NCA_ID_4 = "test-authorized-nca-id-4";
    public static final String TEST_AUTHORIZED_NCA_ID_5 = "test-authorized-nca-id-5";
    public static final String TEST_AUTHORIZED_NCA_ID_6 = "test-authorized-nca-id-6";

    public static final UUID TEST_FUNCTION_ID = UUID.fromString(
            "571114ac-13a2-4243-8f9f-1ccbee3b374f");
    public static final String TEST_FUNCTION_NAME = "test-function-name";
    public static final UUID TEST_FUNCTION_ID_2 = UUID.fromString(
            "bcaf4c3f-e3dd-464c-a3c7-5c7143555b22");
    public static final String TEST_FUNCTION_NAME_2 = "test-function-name-2";
    public static final UUID TEST_FUNCTION_ID_3 = UUID.fromString(
            "4ab4a3bf-f151-43b9-bf98-80c1b2f68647");
    public static final String TEST_FUNCTION_NAME_3 = "test-function-name-3";

    public static final UUID TEST_DEPLOYMENT_ID = UUID.fromString(
            "c20f84c2-ae85-4bb1-848f-6c47f3f5ff0d");
    public static final UUID TEST_DEPLOYMENT_ID_2 = UUID.fromString(
            "e771477b-de12-454e-804b-3cb3726a0377");

    public static final UUID TEST_VERSION_ID_1 = UUID.fromString(
            "40bf77b3-c59f-42ed-bbb9-32ac2092d70b");
    public static final UUID TEST_VERSION_ID_2 = UUID.fromString(
            "0d11427d-6fc3-408d-bf6a-3c1c93b2861f");
    public static final UUID TEST_VERSION_ID_3 = UUID.fromString(
            "f27a5200-1e64-4cac-9aa1-9839114681cb");
    public static final UUID TEST_VERSION_ID_4 = UUID.fromString(
            "faba5200-1e64-4cac-9aa1-9839114681ff");
    public static final UUID TEST_VERSION_ID_5 = UUID.fromString(
            "faba5200-abcd-4cac-9aa1-9839114681ff");
    public static final String TEST_PUBLIC_FUNCTION_NCA_ID = "test-public-func-nca-id";
    public static final String TEST_PUBLIC_FUNCTION_CLIENT_ID = "test-public-func-client-id";
    public static final UUID TEST_PUBLIC_FUNCTION_ID_1 = UUID.fromString(
            "f4ed636d-9a7b-4d0d-9b2b-fde0d783d2e8");
    public static final UUID TEST_PUBLIC_FUNCTION_VERSION_ID_1 = UUID.fromString(
            "03327590-6381-4651-aafb-4d94faa0d772");
    public static final String TEST_PUBLIC_FUNCTION_NAME_V1 = "test-public-function-name-v1";
    public static final UUID TEST_PUBLIC_FUNCTION_VERSION_ID_2 = UUID.fromString(
            "731446b8-aff7-49cc-a3fb-27081593fad8");
    public static final String TEST_PUBLIC_FUNCTION_NAME_V2 = "test-public-function-name-v2";
    public static final UUID TEST_TELEMETRY_LOGS_ID = UUID.fromString(
            "4df8cb5b-94e0-4fcc-ac12-ddb8da2f25aa");
    public static final UUID TEST_TELEMETRY_METRICS_ID = UUID.fromString(
            "d49695b0-86b1-4a84-9b82-2ea823f51d78");
    public static final UUID TEST_TELEMETRY_TRACES_ID = UUID.fromString(
            "edc4cd0a-80cd-4a7b-90d9-706b9eae9b4c");
    public static final String TEST_TELEMETRY_ENDPOINT =
            "http://example-telemetry.test.com/endpoint";
    public static final UUID TEST_GPU_SPEC_ID = UUID.fromString(
            "144b2372-7d32-4952-aa0f-62dae774dee8");
    public static final UUID TEST_GPU_SPEC_ID_2 = UUID.fromString(
            "64137d55-8354-4fa6-a984-7ddbdcf0cb70");

    // Backends or Cluster Groups
    public static final String GFN = "GFN";
    public static final String OCI = "OCI";


    // GPUs
    public static final String T10 = "T10";
    public static final String L40G = "L40G";
    public static final String A10G = "A10G";

    // Instance Types
    public static final String T10_INSTANCE_TYPE = "g6.full";
    public static final String L40G_INSTANCE_TYPE = "gl40g_1.br25_2xlarge";
    public static final String OCI_L40G_INSTANCE_TYPE = "BM_GPU_L40G-2X";

    public static final UUID FAKE_FUNCTION_ID =
            UUID.fromString("c4ec1ef3-2514-4189-9bb0-3f39c9eeff6e");
    public static final UUID FAKE_VERSION_ID =
            UUID.fromString("73b51046-2ad0-4b12-a40b-ae03976b6385");
    public static final String TEST_FUNCTION_VERSION = "9.9.9";
    public static final String TEST_CONTAINER_ARGS = "test-container-args";

    public static final String TEST_NGC_CONTAINER_REGISTRY = "stg.nvcr.io";
    public static final String TEST_NCG_CONTAINER_REGISTRY_PROD = "nvcr.io";
    public static final String TEST_NGC_CONTAINER_REGISTRY_CANARY = "canary.nvcr.io";
    public static final String TEST_NGC_HELM_REGISTRY = "helm.stg.ngc.nvidia.com";
    public static final String TEST_NGC_HELM_REGISTRY_PROD = "helm.ngc.nvidia.com";
    public static final String TEST_NGC_HELM_REGISTRY_CANARY = "helm.canary.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY = "api.stg.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY_PROD = "api.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY_CANARY = "api.canary.ngc.nvidia.com";
    public static final String TEST_DOCKER_REGISTRY = "docker.io";

    public static final URI TEST_NGC_HELM_CHART =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_NGC_HELM_REGISTRY,
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_NGC_HELM_CHART_WITH_CANARY_HOST =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz"
                               .formatted(TEST_NGC_HELM_REGISTRY_CANARY,
                                          TEST_VALID_ORG_NAME,
                                          TEST_VALID_TEAM_NAME,
                                          TEST_VALID_HELM_CHART_NAME,
                                          TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_HELM_CHART_UNKNOWN_REGISTRY =
            URI.create(
                    "https://unknown-registry/%s/%s/charts/%s-%s.tgz"
                            .formatted(TEST_VALID_ORG_NAME,
                                       TEST_VALID_TEAM_NAME,
                                       TEST_VALID_HELM_CHART_NAME,
                                       TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_NGC_HELM_CHART_NOT_EXISTS =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_NGC_HELM_REGISTRY,
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     "invalid-helm-chart",
                                                                     TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_NGC_HELM_CHART_PERMISSION_DENIED =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_NGC_HELM_REGISTRY,
                                                                     TEST_UNKNOWN_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_UNKNOWN_HELM_CHART_VERSION));
    public static final URI TEST_HELM_CHART_NOT_SUPPORTED_REGISTRY =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted("not.support.com",
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_VALID_HELM_CHART_VERSION));
    public static final String TEST_HELM_CHART_SERVICE_NAME = "ENTRYPOINT";
    public static final URI TEST_NGC_CONTAINER_IMAGE =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_NGC_CONTAINER_IMAGE_WITH_CANARY_HOST =
            URI.create(
                    TEST_NGC_CONTAINER_REGISTRY_CANARY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                               TEST_VALID_CONTAINER_NAME,
                                                                               TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY =
            URI.create("not-exits/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                      TEST_VALID_CONTAINER_NAME,
                                                      TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_NGC_CONTAINER_IMAGE_WITHOUT_TAG =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s".formatted(TEST_VALID_ORG_NAME,
                                                                        TEST_VALID_CONTAINER_NAME));
    public static final URI TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s@%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           TEST_VALID_CONTAINER_HASH));
    public static final URI TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           "not-exists"));
    public static final URI TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           "latest:latest"));
    public static final URI TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           "permission-denied"));
    public static final URI TEST_CONTAINER_IMAGE_NOT_SUPPORTED =
            URI.create("not.supported" + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                               TEST_VALID_CONTAINER_NAME,
                                                               TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_INFERENCE_URL = URI.create("test-inference-url");
    public static final URI NORMALIZED_TEST_INFERENCE_URL = URI.create("/test-inference-url");
    public static final Integer TEST_INFERENCE_PORT = 7777;
    public static final String BASE_ARTIFACT_URL = "https://" + TEST_NGC_ARTIFACT_REGISTRY;
    public static final String TEST_MODEL_URL_1 = BASE_ARTIFACT_URL + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_2 =
            BASE_ARTIFACT_URL + MODEL_SMALL_SIZE_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_WITH_CANARY_HOST =
            "https://" + TEST_NGC_ARTIFACT_REGISTRY_CANARY + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_WITH_VERSIONS_1 =
            BASE_ARTIFACT_URL + MODEL_FILES_URL_WITH_VERSION;
    public static final String TEST_MODEL_URL_END_WITH_ZIP_1 =
            BASE_ARTIFACT_URL + "/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/zip";
    public static final String TEST_MODEL_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_MISSING_PROTOCOL_1 =
            TEST_NGC_ARTIFACT_REGISTRY + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_NOT_SUPPORTED_REGISTRY_1 =
            "https://not.support.com" + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_MODEL_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_ARTIFACT_URL + MODEL_FILE_PERMISSION_DENIED_URL;
    public static final String TEST_MODEL_URL_NOT_EXISTS_1 =
            BASE_ARTIFACT_URL + MODEL_FILES_NOT_EXIST_URL;
    public static final String TEST_RESOURCE_URL_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_WITH_CANARY_HOST_1 =
            "https://" + TEST_NGC_ARTIFACT_REGISTRY_CANARY + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_END_WITH_ZIP_1 =
            BASE_ARTIFACT_URL + "/v2/org/whw3rcpsilnj/resources/playground_llama2_trt_l40g/0.1/zip";
    public static final String TEST_RESOURCE_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_MISSING_PROTOCOL_1 =
            TEST_NGC_ARTIFACT_REGISTRY + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_NOT_SUPPORTED_REGISTRY_1 =
            "https://not.support.com" + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_NOT_EXISTS_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM;

    public static final String SCOPE_REGISTER_FUNCTION = "register_function";
    public static final String SCOPE_UPDATE_FUNCTION = "update_function";
    public static final String SCOPE_DELETE_FUNCTION = "delete_function";
    public static final String SCOPE_DEPLOY_FUNCTION = "deploy_function";
    public static final String SCOPE_LIST_FUNCTIONS = "list_functions";
    public static final String SCOPE_INVOKE_FUNCTION = "invoke_function";
    public static final String SCOPE_QUEUE_DETAILS = "queue_details";
    public static final String SCOPE_ACCOUNT_SETUP = "account_setup";
    public static final String SCOPE_LIST_CLUSTER_GROUPS = "list_cluster_groups";
    public static final Duration HEALTH_TIMEOUT = Duration.ofSeconds(10);
    public static final int EXPECTED_STATUS_CODE = 200;
    public static final int DEFAULT_MAX_FUNCTIONS_ALLOWED = 50;
    public static final int DEFAULT_MAX_TASKS_ALLOWED = 50;

    public static final List<FunctionModelDto> TEST_MODEL_DTOS =
            List.of(FunctionModelDto.builder()
                            .name("model-1").version("1.0")
                            .uri(URI.create(TEST_MODEL_URL_1))
                            .build(),
                    FunctionModelDto.builder()
                            .name("model-2").version("2.0")
                            .uri(URI.create(TEST_MODEL_URL_1))
                            .build());
    public static final Set<ArtifactDto> TEST_RESOURCE_DTOS =
            Set.of(ArtifactDto.builder()
                           .name("resource-1").version("1.0")
                           .uri(URI.create(TEST_RESOURCE_URL_1))
                           .build(),
                   ArtifactDto.builder()
                           .name("resource-2").version("2.0")
                           .uri(URI.create(TEST_RESOURCE_URL_1))
                           .build());
    public static final Set<ResourceUdt> TEST_RESOURCES =
            Set.of(ResourceUdt.builder()
                           .name("resource-1").version("1.0")
                           .url(TEST_RESOURCE_URL_1)
                           .build(),
                   ResourceUdt.builder()
                           .name("resource-2").version("2.0")
                           .url(TEST_RESOURCE_URL_1)
                           .build());
    public static final SecretDto TEST_TELEMETRY_LOG_SECRETS =
            SecretDto.builder()
                    .name("telemetry-log-secret-name")
                    .value(new StringNode("telemetry-log-secret-value"))
                    .build();
    public static final SecretDto TEST_TELEMETRY_METRICS_SECRETS =
            SecretDto.builder()
                    .name("telemetry-metrics-secret-name")
                    .value(new StringNode("telemetry-metrics-secret-value"))
                    .build();
    public static final SecretDto TEST_TELEMETRY_TRACES_SECRETS =
            SecretDto.builder()
                    .name("telemetry-traces-secret-name")
                    .value(new StringNode("telemetry-traces-secret-value"))
                    .build();

    public static final SecretDto TEST_THIRD_PARTY_MODEL_SECRET_1 = SecretDto.builder()
            .name("test-model-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:nvapi-stg-test-model-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_MODEL_SECRET_1_UPDATED = SecretDto.builder()
            .name("test-model-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:nvapi-stg-test-model-secret-val-1-updated"
                            .getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_BASE64_ENCODED =
            SecretDto.builder()
                    .name("test-model-secret-1")
                    .value(new StringNode("$oauthtoken:nvapi-stg-test-model-secret-val-1"))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_OAUTH_TOKEN =
            SecretDto.builder()
                    .name("test-model-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "missing:nvapi-stg-test-model-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_USER_NAME =
            SecretDto.builder()
                    .name("test-model-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "nvapi-stg-test-model-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_RESOURCE_SECRET_1 = SecretDto.builder()
            .name("test-resource-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-resource-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_RESOURCE_SECRET_1_UPDATED = SecretDto.builder()
            .name("test-resource-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-resource-secret-val-1-updated".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_BASE64_ENCODED =
            SecretDto.builder()
                    .name("test-resource-secret-1")
                    .value(new StringNode("$oauthtoken:nvapi-stg-test-resource-secret-val-1"))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_OAUTH_TOKEN =
            SecretDto.builder()
                    .name("test-resource-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "missing:nvapi-stg-test-resource-secret-val-1".getBytes(UTF_8))))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_USER_NAME =
            SecretDto.builder()
                    .name("test-resource-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "nvapi-stg-test-resource-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_CONTAINER_SECRET_1 = SecretDto.builder()
            .name("test-container-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-container-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_CONTAINER_SECRET_1_UPDATED = SecretDto.builder()
            .name("test-container-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-container-secret-val-1-updated".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_BASE64_ENCODED =
            SecretDto.builder()
                    .name("test-container-secret-1")
                    .value(new StringNode("$oauthtoken:nvapi-stg-test-container-secret-val-1"))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_OAUTH_TOKEN =
            SecretDto.builder()
                    .name("test-container-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "missing:nvapi-stg-test-container-secret-val-1".getBytes(UTF_8))))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_USER_NAME =
            SecretDto.builder()
                    .name("test-container-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "nvapi-stg-test-container-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_HELM_SECRET_1 = SecretDto.builder()
            .name("test-helm-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-helm-secret-val-1".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_HELM_SECRET_1_UPDATED = SecretDto.builder()
            .name("test-helm-secret-1")
            .value(new StringNode(Base64.getEncoder().encodeToString(
                    "$oauthtoken:test-helm-secret-val-1-updated".getBytes(UTF_8)))).build();
    public static final SecretDto TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_BASE64_ENCODED =
            SecretDto.builder()
                    .name("test-helm-secret-1")
                    .value(new StringNode("$oauthtoken:nvapi-stg-test-helm-secret-val-1"))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_OAUTH_TOKEN =
            SecretDto.builder()
                    .name("test-helm-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "missing:nvapi-stg-test-helm-secret-val-1".getBytes(UTF_8))))
                    .build();
    public static final SecretDto TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_USER_NAME =
            SecretDto.builder()
                    .name("test-helm-secret-1")
                    .value(new StringNode(Base64.getEncoder().encodeToString(
                            "nvapi-stg-test-helm-secret-val-1".getBytes(UTF_8)))).build();
    public static final String MOCK_RAW_NGC_CONTAINER_CRED_1 =
            "$oauthtoken:nvapi-stg-test-ngc-container-secret-val-1";
    public static final String BASE64_ENCODED_NGC_CONTAINER_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_CONTAINER_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_CONTAINER_SECRET_1 = SecretDto.builder()
            .name("test-ngc-container-secret-1")
            .value(new StringNode(BASE64_ENCODED_NGC_CONTAINER_CRED_1))
            .build();
    public static final String MOCK_RAW_NGC_CONTAINER_CRED_2 =
            "$oauthtoken:nvapi-stg-test-ngc-container-secret-val-2";
    public static final String BASE64_ENCODED_NGC_CONTAINER_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_CONTAINER_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_CONTAINER_SECRET_2 = SecretDto.builder()
            .name("test-ngc-container-secret-2")
            .value(new StringNode(BASE64_ENCODED_NGC_CONTAINER_CRED_2))
            .build();
    public static final String MOCK_RAW_NGC_HELM_CRED_1 =
            "$oauthtoken:nvapi-stg-test-ngc-helm-secret-val-1";
    public static final String BASE64_ENCODED_NGC_HELM_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_HELM_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_HELM_SECRET_1 = SecretDto.builder()
            .name("test-ngc-helm-secret-1")
            .value(new StringNode(BASE64_ENCODED_NGC_HELM_CRED_1))
            .build();
    public static final String MOCK_RAW_NGC_HELM_CRED_2 =
            "$oauthtoken:nvapi-stg-test-ngc-helm-secret-val-2";
    public static final String BASE64_ENCODED_NGC_HELM_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_HELM_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_HELM_SECRET_2 = SecretDto.builder()
            .name("test-ngc-helm-secret-2")
            .value(new StringNode(BASE64_ENCODED_NGC_HELM_CRED_2))
            .build();
    public static final String MOCK_RAW_NGC_HELM_LEGACY_CRED_1 =
            "$oauthtoken:legacy-test-helm-api-key-1";
    public static final String BASE64_ENCODED_NGC_HELM_LEGACY_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_HELM_LEGACY_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_HELM_LEGACY_SECRET_1 = SecretDto.builder()
            .name("ngc-helm-legacy-key-1")
            .value(new StringNode(BASE64_ENCODED_NGC_HELM_LEGACY_CRED_1))
            .build();
    public static final String MOCK_RAW_NGC_HELM_LEGACY_CRED_2 =
            "$oauthtoken:legacy-test-helm-api-key-2";
    public static final String BASE64_ENCODED_NGC_HELM_LEGACY_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_HELM_LEGACY_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_NGC_HELM_LEGACY_SECRET_2 = SecretDto.builder()
            .name("ngc-helm-legacy-key-2")
            .value(new StringNode(BASE64_ENCODED_NGC_HELM_LEGACY_CRED_2))
            .build();
    public static final String MOCK_RAW_DOCKER_HUB_CRED_1 =
            "docker-username-test-1:docker-password-test-1";
    public static final String BASE64_ENCODED_DOCKER_HUB_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_DOCKER_HUB_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_DOCKER_HUB_SECRET_1 = SecretDto.builder()
            .name("test-docker-secret-1")
            .value(new StringNode(BASE64_ENCODED_DOCKER_HUB_CRED_1))
            .build();
    public static final String MOCK_RAW_DOCKER_HUB_CRED_2 =
            "docker-username-test-2:docker-password-test-2";
    public static final String BASE64_ENCODED_DOCKER_HUB_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_DOCKER_HUB_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_DOCKER_HUB_SECRET_2 = SecretDto.builder()
            .name("test-docker-secret-2")
            .value(new StringNode(BASE64_ENCODED_DOCKER_HUB_CRED_2))
            .build();
    public static final String MOCK_RAW_ECR_PRIVATE_CRED_1 =
            TEST_VALID_ECR_ACCESS_KEY_ID + ":" + TEST_VALID_ECR_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_ECR_PRIVATE_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_PRIVATE_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_ECR_PRIVATE_SECRET_1 = SecretDto.builder()
            .name("test-ecr-private-secret-1")
            .value(new StringNode(BASE64_ENCODED_ECR_PRIVATE_CRED_1))
            .build();
    public static final String MOCK_RAW_ECR_PRIVATE_CRED_2 =
            TEST_VALID_ECR_ACCESS_KEY_ID + ":" + "ecr_secret_access_key_test_2";
    public static final String BASE64_ENCODED_ECR_PRIVATE_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_PRIVATE_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_ECR_PRIVATE_SECRET_2 = SecretDto.builder()
            .name("test-ecr-private-secret-2")
            .value(new StringNode(BASE64_ENCODED_ECR_PRIVATE_CRED_2))
            .build();
    public static final String MOCK_INVALID_RAW_ECR_PRIVATE_CRED_1 =
            TEST_INVALID_ECR_ACCESS_KEY_ID + ":" + TEST_INVALID_ECR_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_INVALID_ECR_PRIVATE_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_INVALID_RAW_ECR_PRIVATE_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_INVALID_ECR_PRIVATE_SECRET_1 = SecretDto.builder()
            .name("test-invalid-ecr-private-secret-1")
            .value(new StringNode(BASE64_ENCODED_INVALID_ECR_PRIVATE_CRED_1))
            .build();
    public static final String MOCK_RAW_ECR_PUBLIC_CRED_1 =
            TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID + ":" + TEST_VALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_ECR_PUBLIC_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_PUBLIC_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_ECR_PUBLIC_SECRET_1 = SecretDto.builder()
            .name("test-ecr-public-secret-1")
            .value(new StringNode(BASE64_ENCODED_ECR_PUBLIC_CRED_1))
            .build();
    public static final String MOCK_RAW_ECR_PUBLIC_CRED_2 =
            TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID + ":" + "ecr_public_secret_access_key_test_1";
    public static final String BASE64_ENCODED_ECR_PUBLIC_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_PUBLIC_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_ECR_PUBLIC_SECRET_2 = SecretDto.builder()
            .name("test-ecr-public-secret-2")
            .value(new StringNode(BASE64_ENCODED_ECR_PUBLIC_CRED_2))
            .build();
    public static final String MOCK_INVALID_RAW_ECR_PUBLIC_CRED_1 =
            TEST_INVALID_ECR_PUBLIC_ACCESS_KEY_ID + ":" + TEST_INVALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_INVALID_ECR_PUBLIC_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_INVALID_RAW_ECR_PUBLIC_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_INVALID_ECR_PUBLIC_SECRET_1 = SecretDto.builder()
            .name("test-invalid-ecr-public-secret-1")
            .value(new StringNode(BASE64_ENCODED_INVALID_ECR_PUBLIC_CRED_1))
            .build();
    public static final String MOCK_RAW_VOLCENGINE_CRED_1 =
            TEST_VALID_VOLCENGINE_ACCESS_KEY_ID + ":" + TEST_VALID_VOLCENGINE_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_VOLCENGINE_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_VOLCENGINE_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_VOLCENGINE_SECRET_1 = SecretDto.builder()
            .name("test-volcengine-secret-1")
            .value(new StringNode(BASE64_ENCODED_VOLCENGINE_CRED_1))
            .build();
    public static final String MOCK_RAW_VOLCENGINE_CRED_2 =
            TEST_VALID_VOLCENGINE_ACCESS_KEY_ID + ":" + "volcengine_secret_access_key_test_2";
    public static final String BASE64_ENCODED_VOLCENGINE_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_VOLCENGINE_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_VOLCENGINE_SECRET_2 = SecretDto.builder()
            .name("test-volcengine-secret-2")
            .value(new StringNode(BASE64_ENCODED_VOLCENGINE_CRED_2))
            .build();
    public static final String MOCK_INVALID_RAW_VOLCENGINE_CRED_1 =
            TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID + ":" + TEST_INVALID_VOLCENGINE_SECRET_ACCESS_KEY;
    public static final String BASE64_ENCODED_INVALID_VOLCENGINE_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_INVALID_RAW_VOLCENGINE_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_INVALID_VOLCENGINE_SECRET_1 = SecretDto.builder()
            .name("test-invalid-volcengine-secret-1")
            .value(new StringNode(BASE64_ENCODED_INVALID_VOLCENGINE_CRED_1))
            .build();
    public static final String MOCK_RAW_ACR_CRED_1 =
            "acr-client-id-test-1:acr-client-secret-test-1";
    public static final String BASE64_ENCODED_ACR_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ACR_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_ACR_SECRET_1 = SecretDto.builder()
            .name("test-acr-secret-1")
            .value(new StringNode(BASE64_ENCODED_ACR_CRED_1))
            .build();
    public static final String MOCK_RAW_ACR_CRED_2 =
            "acr-client-id-test-2:acr-client-secret-test-2";
    public static final String BASE64_ENCODED_ACR_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ACR_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_ACR_SECRET_2 = SecretDto.builder()
            .name("test-acr-secret-2")
            .value(new StringNode(BASE64_ENCODED_ACR_CRED_2))
            .build();
    public static final String MOCK_RAW_HARBOR_CRED_1 =
            "harbor-username-test-1:harbor-password-test-1";
    public static final String BASE64_ENCODED_HARBOR_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_HARBOR_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_HARBOR_SECRET_1 = SecretDto.builder()
            .name("test-harbor-secret-1")
            .value(new StringNode(BASE64_ENCODED_HARBOR_CRED_1))
            .build();
    public static final String MOCK_RAW_HARBOR_CRED_2 =
            "harbor-username-test-2:harbor-password-test-2";
    public static final String BASE64_ENCODED_HARBOR_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_HARBOR_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_HARBOR_SECRET_2 = SecretDto.builder()
            .name("test-harbor-secret-2")
            .value(new StringNode(BASE64_ENCODED_HARBOR_CRED_2))
            .build();
    public static final String MOCK_RAW_ARTIFACTORY_CRED_1 =
            "artifactory-username-test-1:artifactory-token-test-1";
    public static final String BASE64_ENCODED_ARTIFACTORY_CRED_1 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ARTIFACTORY_CRED_1.getBytes(UTF_8));
    public static final SecretDto TEST_ARTIFACTORY_SECRET_1 = SecretDto.builder()
            .name("test-artifactory-secret-1")
            .value(new StringNode(BASE64_ENCODED_ARTIFACTORY_CRED_1))
            .build();
    public static final String MOCK_RAW_ARTIFACTORY_CRED_2 =
            "artifactory-username-test-2:artifactory-token-test-2";
    public static final String BASE64_ENCODED_ARTIFACTORY_CRED_2 =
            Base64.getEncoder().encodeToString(MOCK_RAW_ARTIFACTORY_CRED_2.getBytes(UTF_8));
    public static final SecretDto TEST_ARTIFACTORY_SECRET_2 = SecretDto.builder()
            .name("test-artifactory-secret-2")
            .value(new StringNode(BASE64_ENCODED_ARTIFACTORY_CRED_2))
            .build();

    public static final Key<String> MD_KEY_AUTHORIZATION = Key.of("authorization",
                                                                  Metadata.ASCII_STRING_MARSHALLER);
    public static final Set<String> TEST_TAGS = Set.of("tag1", "tag2", "tag3");
    public static final Set<String> TEST_TAGS_2 = Set.of("tag1", "tag2", "tag3", "tag4");

    public static final String TEST_DESCRIPTION = "test-description";

    public static final String TEST_CPU_ARCH = "x86";
    public static final String TEST_OS = "Linux 64 bit";
    public static final String TEST_DRIVER_VERSION = "v.353.145.36";
    public static final String TEST_STORAGE = "80G";
    public static final Set<String> SUPPORTED_INSTANCE_TYPES =
            Set.of(T10_INSTANCE_TYPE, L40G_INSTANCE_TYPE);
    public static final String TEST_SYSTEM_MEMORY = "64G";
    public static final String TEST_GPU_MEMORY = "48G";

    public static final RegistryCredentialDto TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .registryHostname("stg.nvcr.io")
                    .secret(SecretDto.builder()
                                    .name("ngc-container-registry-credential")
                                    .value(new StringNode(BASE64_CONTAINER_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_NGC_MODEL_REGISTRY_CREDENTIAL =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                    .registryHostname("api.stg.ngc.nvidia.com")
                    .secret(SecretDto.builder()
                                    .name("ngc-model-registry-credential")
                                    .value(new StringNode(BASE64_MODEL_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_NGC_HELM_REGISTRY_CREDENTIAL =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                    .registryHostname("helm.stg.ngc.nvidia.com")
                    .secret(SecretDto.builder()
                                    .name("ngc-helm-registry-credential")
                                    .value(new StringNode(BASE64_HELM_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                    .registryHostname("api.stg.ngc.nvidia.com")
                    .secret(SecretDto.builder()
                                    .name("ngc-resource-registry-credential")
                                    .value(new StringNode(BASE64_RESOURCE_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.MODEL, ArtifactTypeEnum.RESOURCE))
                    .registryHostname("api.stg.ngc.nvidia.com")
                    .secret(SecretDto.builder()
                                    .name("ngc-model-resource-registry-credential")
                                    .value(new StringNode(BASE64_MODEL_REGISTRY_CRED))
                                    .build())
                    .build();

    public static final RegistryCredentialDto TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .registryHostname("docker.io")
                    .secret(TEST_DOCKER_HUB_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_DOCKER_CONTAINER_REGISTRY_MISSING_BASE64_CREDS =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .registryHostname("docker.io")
                    .secret(SecretDto.builder()
                                    .name("cred-for-docker-acct-foo")
                                    .value(new StringNode("username:password"))
                                    .build())
                    .build();
    public static final String BASE64_ENCODED_INVALID_FORMAT_DOCKER_CRED = Base64.getEncoder()
            .encodeToString("username+password".getBytes(UTF_8));
    public static final RegistryCredentialDto TEST_DOCKER_CONTAINER_REGISTRY_INVALID_FORMAT =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .registryHostname("docker.io")
                    .secret(SecretDto.builder()
                                    .name("cred-for-docker-acct-foo")
                                    .value(new StringNode(BASE64_ENCODED_INVALID_FORMAT_DOCKER_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_ECR_PRIVATE_CONTAINER_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("123456789110.dkr.ecr.us-west-2.amazonaws.com")
                    .secret(TEST_ECR_PRIVATE_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_ECR_PUBLIC_CONTAINER_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("public.ecr.aws")
                    .secret(TEST_ECR_PUBLIC_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_VOLCENGINE_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("test1-cn-shanghai.cr.volces.com")
                    .secret(TEST_VOLCENGINE_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_ACR_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("test1.azurecr.io")
                    .secret(TEST_ACR_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_HARBOR_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("demo.goharbor.io")
                    .secret(TEST_HARBOR_SECRET_1)
                    .build();
    public static final RegistryCredentialDto TEST_ARTIFACTORY_REGISTRY_CREDENTIAL =
            RegistryCredentialDto
                    .builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                    .registryHostname("artifactorytest12345.jfrog.io")
                    .secret(TEST_ARTIFACTORY_SECRET_1)
                    .build();

    public static final String TEST_CUSTOM_REGISTRY_NAME_1 = "custom-1";
    public static final String TEST_CUSTOM_REGISTRY_DISPLAY_NAME_1 = "Custom Registry Test 1";
    public static final String TEST_CUSTOM_REGISTRY_HOST_NAME_1 = "custom-registry-test-1.com";
    public static final URI TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1 =
            URI.create(TEST_CUSTOM_REGISTRY_HOST_NAME_1 + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_CUSTOM_HELM_CHART_WITH_TAG_1 =
            URI.create("oci://" + TEST_CUSTOM_REGISTRY_HOST_NAME_1 + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME, TEST_VALID_OCI_HELM_CHART_TAG_NAME));

    public static final String TEST_INSTANCE_ID = "test-instance-id";
}
