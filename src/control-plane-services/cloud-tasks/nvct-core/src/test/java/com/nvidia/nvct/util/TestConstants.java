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
package com.nvidia.nvct.util;

import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_HELM_CHART_TAG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_IMAGE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_OCI_IMAGE_TAG_NAME;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_NOT_EXIST_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_TEAM_2;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_VERSION;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILE_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL_WITH_TEAM_2;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL_WITH_VERSION;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static java.nio.charset.StandardCharsets.UTF_8;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.mock.BootTestConstants;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.ModelUdt;
import com.nvidia.nvct.persistence.task.entity.ResourceUdt;
import com.nvidia.nvct.rest.task.dto.ArtifactDto;
import com.nvidia.nvct.rest.task.dto.ContainerEnvironmentEntryDto;
import com.nvidia.nvct.rest.task.dto.GpuSpecificationDto;
import com.nvidia.nvct.rest.task.dto.HelmValidationPolicyDto;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.rest.task.dto.ValidationPolicyNameEnum;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import io.grpc.Metadata;
import io.grpc.Metadata.Key;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.List;
import java.util.Set;
import java.util.UUID;

public class TestConstants {

    private static final JsonMapper OBJECT_MAPPER = JsonMapper.builder().build();

    public static final String TEST_ADMIN_SUBJECT = "test-admin-id";

    public static final String TEST_CLIENT_SUBJECT = "test-client-id";
    public static final String TEST_ACCOUNT_NAME = "test-account";
    public static final String TEST_NCA_ID = "test-nca-id";
    public static final String TEST_OWNER_ID = "test-owner-id";
    public static final String TEST_OWNER_ID_2 = "test-owner-id-2";
    public static final String TEST_CLIENT_ID = TEST_CLIENT_SUBJECT;
    public static final String TEST_CLIENT_SUBJECT_2 = "test-client-id-2";
    public static final String TEST_ACCOUNT_NAME_2 = "test-account-2";
    public static final String TEST_NCA_ID_2 = "test-nca-id-2";
    public static final String TEST_CLIENT_ID_2 = TEST_CLIENT_SUBJECT_2;
    public static final String TEST_ACCOUNT_NAME_3 = "test-account-3";
    public static final String TEST_NCA_ID_3 = "test-nca-id-3";
    public static final String TEST_CLIENT_ID_3 = "test-client-id-3";
    public static final String TEST_NCA_ID_WITH_TELEMETRIES_4 = "test-nca-id-with-telemetries-4";
    public static final String TEST_CLIENT_ID_4 = "test-client-id-4";
    public static final String TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5 =
            "test-nca-id-with-1-max-allowed-tasks-5";
    public static final String TEST_CLIENT_ID_5 = "test-client-id-5";
    public static final String TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6 =
            "test-nca-id-without-registry-credentials-6";
    public static final String TEST_CLIENT_ID_6 = "test-client-id-6";

    public static final UUID TEST_TASK_ID_1 =
            UUID.fromString("571114ac-13a2-4243-8f9f-1ccbee3b374f");
    public static final String TEST_TASK_NAME_1 = "test-task-name-1";
    public static final UUID TEST_TASK_ID_2 =
            UUID.fromString("bcaf4c3f-e3dd-464c-a3c7-5c7143555b22");
    public static final String TEST_TASK_NAME_2 = "test-task-name-2";
    public static final UUID TEST_TASK_ID_3 =
            UUID.fromString("4ab4a3bf-f151-43b9-bf98-80c1b2f68647");
    public static final UUID TEST_TASK_ID_4 =
            UUID.fromString("5bc4a3bf-f151-43b9-bf98-80c1b2f68647");
    public static final UUID TEST_TASK_ID_5 =
            UUID.fromString("6ef4a3bf-f151-43b9-bf98-80c1b2f68647");
    public static final String TEST_TASK_NAME_3 = "test-task-name-3";
    public static final UUID FAKE_TASK_ID =
            UUID.fromString("c4ec1ef3-2514-4189-9bb0-3f39c9eeff6e");


    public static final UUID EVENT_ID_1 = UUID.randomUUID();
    public static final UUID EVENT_ID_2 = UUID.randomUUID();
    public static final UUID EVENT_ID_3 = UUID.randomUUID();
    public static final UUID EVENT_ID_4 = UUID.randomUUID();

    public static final UUID RESULT_ID_1 = UUID.randomUUID();
    public static final UUID RESULT_ID_2 = UUID.randomUUID();
    public static final UUID RESULT_ID_3 = UUID.randomUUID();
    public static final UUID RESULT_ID_4 = UUID.randomUUID();

    public static final String TEST_VALID_ORG_NAME = "whw3rcpsilnj"; // "test-valid-org-name"
    public static final String TEST_VALID_ORG_NAME_INVALID_KEY = "test-valid-org-invalid-key";
    public static final String TEST_VALID_TEAM_NAME = "jeff";
    public static final String TEST_UNKNOWN_ORG_NAME = "test-unknown-org-name";
    public static final String TEST_UNKNOWN_TEAM_NAME = "test-unknown-team-name";
    public static final String TEST_MODEL_NAME = "test-model-name";
    public static final String TEST_NGC_API_KEY = "nvapi-stg-test-key";

    public static final String TEST_RESULTS_LOCATION_1 =
            TEST_VALID_ORG_NAME + "/" + TEST_VALID_TEAM_NAME + "/" + TEST_MODEL_NAME;
    public static final String TEST_RESULTS_LOCATION_2 =
            TEST_VALID_ORG_NAME + "/" + TEST_MODEL_NAME;

    public static final UUID TEST_ICMS_REQ_ID_1 =
            UUID.fromString("572114ac-13a2-4243-8f9f-1ccbee3b374f");
    public static final UUID TEST_ICMS_REQ_ID_2 =
            UUID.fromString("672114ad-13a2-4243-8f9f-1ccbee3b374f");
    public static final UUID TEST_ICMS_REQ_ID_3 =
            UUID.fromString("772114ae-13a2-4243-8f9f-1ccbee3b374f");
    public static final UUID TEST_ICMS_REQ_ID_4 =
            UUID.fromString("872114ae-13a2-4243-8f9f-1ccbee3b374f");
    public static final UUID TEST_ICMS_REQ_ID_5 =
            UUID.fromString("972114ae-13a2-4243-8f9f-1ccbee3b374f");

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

    public static final GpuSpecificationDto TEST_GFN_GPU_SPEC_DTO =
            GpuSpecificationDto.builder().gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                    .backend(GFN).build();
    public static final GpuSpecificationDto TEST_OCI_GPU_SPEC_DTO =
            GpuSpecificationDto.builder().gpu(L40G).instanceType(OCI_L40G_INSTANCE_TYPE)
                    .backend(OCI).build();
    public static final GpuSpecUdt TEST_GFN_GPU_SPEC =
            GpuSpecUdt.builder()
                    .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN).build();
    private static final ObjectNode CONFIGURATION = OBJECT_MAPPER.createObjectNode()
            .put("replicas", 5);
    public static final GpuSpecificationDto TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO =
            GpuSpecificationDto.builder().gpu(L40G).instanceType(OCI_L40G_INSTANCE_TYPE)
                    .backend(OCI).configuration(CONFIGURATION).build();
    public static final HelmValidationPolicyDto TEST_HELM_VALIDATION_POLICY_DTO =
            HelmValidationPolicyDto.builder()
                    .name(ValidationPolicyNameEnum.DEFAULT)
                    .extraKubernetesTypes(List.of(HelmValidationPolicyDto.KubernetesType.builder()
                                                    .group("apps")
                                                    .version("v1")
                                                    .kind("Deployment")
                                                    .build()))
                    .build();
    public static final GpuSpecificationDto TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO =
            GpuSpecificationDto.builder().gpu(L40G).instanceType(OCI_L40G_INSTANCE_TYPE)
                    .backend(OCI)
                    .helmValidationPolicy(TEST_HELM_VALIDATION_POLICY_DTO)
                    .build();

    public static final String TEST_CONTAINER_REGISTRY = "stg.nvcr.io";
    public static final String TEST_CONTAINER_REGISTRY_PROD = "nvcr.io";
    public static final String TEST_CONTAINER_REGISTRY_CANARY = "canary.nvcr.io";
    public static final String TEST_HELM_REGISTRY = "helm.stg.ngc.nvidia.com";
    public static final String TEST_HELM_REGISTRY_PROD = "helm.ngc.nvidia.com";
    public static final String TEST_HELM_REGISTRY_CANARY = "helm.canary.ngc.nvidia.com";
    public static final String TEST_ARTIFACT_REGISTRY = "api.stg.ngc.nvidia.com";
    public static final String TEST_ARTIFACT_REGISTRY_PROD = "api.ngc.nvidia.com";
    public static final String TEST_ARTIFACT_REGISTRY_CANARY = "api.canary.ngc.nvidia.com";
    public static final String TEST_DOCKER_REGISTRY = "docker.io";
    public static final String TEST_CONTAINER_ARGS = "test-container-args";
    public static final URI TEST_HELM_CHART =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_HELM_REGISTRY,
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_HELM_CHART_WITH_CANARY_HOST =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz"
                               .formatted(TEST_HELM_REGISTRY_CANARY,
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
    public static final URI TEST_HELM_CHART_NOT_EXISTS =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_HELM_REGISTRY,
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_UNKNOWN_HELM_CHART_VERSION));
    public static final URI TEST_HELM_CHART_PERMISSION_DENIED =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(TEST_HELM_REGISTRY,
                                                                     BootTestConstants.TEST_UNKNOWN_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_UNKNOWN_HELM_CHART_VERSION));
    public static final URI TEST_HELM_CHART_NOT_SUPPORTED_REGISTRY =
            URI.create("https://%s/%s/%s/charts/%s-%s.tgz".formatted("not.support.com",
                                                                     TEST_VALID_ORG_NAME,
                                                                     TEST_VALID_TEAM_NAME,
                                                                     TEST_VALID_HELM_CHART_NAME,
                                                                     TEST_VALID_HELM_CHART_VERSION));
    public static final URI TEST_CONTAINER_IMAGE =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                       TEST_VALID_CONTAINER_NAME,
                                                                       TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_CONTAINER_IMAGE_WITH_CANARY_HOST =
            URI.create(TEST_CONTAINER_REGISTRY_CANARY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                              TEST_VALID_CONTAINER_NAME,
                                                                              TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY =
            URI.create("not-exits/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                      TEST_VALID_CONTAINER_NAME,
                                                      TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_CONTAINER_IMAGE_WITHOUT_TAG =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s".formatted(TEST_VALID_ORG_NAME,
                                                                    TEST_VALID_CONTAINER_NAME));
    public static final URI TEST_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s@%s".formatted(TEST_VALID_ORG_NAME,
                                                                       TEST_VALID_CONTAINER_NAME,
                                                                       TEST_VALID_CONTAINER_HASH));
    public static final URI TEST_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                       TEST_VALID_CONTAINER_NAME,
                                                                       "not-exists"));
    public static final URI TEST_CONTAINER_IMAGE_WITH_INVALID_TAG =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                       TEST_VALID_CONTAINER_NAME,
                                                                       "latest:latest"));
    public static final URI TEST_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                       TEST_VALID_CONTAINER_NAME,
                                                                       "permission-denied"));
    public static final URI TEST_CONTAINER_IMAGE_NOT_SUPPORTED =
            URI.create("not.supported" + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                               TEST_VALID_CONTAINER_NAME,
                                                               TEST_VALID_CONTAINER_TAG));
    public static final String BASE_ARTIFACT_URL = "https://" + TEST_ARTIFACT_REGISTRY;

    public static final String MODEL_ARTIFACTS_URL = BASE_ARTIFACT_URL + MODEL_FILES_URL_WITH_TEAM;
    public static final String MODEL_ARTIFACTS_URL_2 =
            BASE_ARTIFACT_URL + MODEL_FILES_URL_WITH_TEAM_2;
    public static final String MODEL_ARTIFACTS_URL_3 = BASE_ARTIFACT_URL + MODEL_FILES_URL;
    public static final String MODEL_ARTIFACTS_URL_4 =
            BASE_ARTIFACT_URL + MODEL_FILES_URL_WITH_VERSION;
    public static final String MODEL_ARTIFACTS_URL_WITH_CANARY_HOST =
            "https://" + TEST_ARTIFACT_REGISTRY_CANARY + MODEL_FILES_URL_WITH_TEAM;
    public static final String MODEL_ARTIFACTS_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + MODEL_FILES_URL_WITH_TEAM;
    public static final String MODEL_ARTIFACTS_URL_MISSING_PROTOCOL_1 =
            TEST_ARTIFACT_REGISTRY + MODEL_FILES_URL_WITH_TEAM;
    public static final String MODEL_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1 =
            "https://not.support.com" + MODEL_FILES_URL_WITH_TEAM;
    public static final String MODEL_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_ARTIFACT_URL + MODEL_FILE_PERMISSION_DENIED_URL;
    public static final String MODEL_ARTIFACTS_URL_NOT_EXISTS_1 =
            BASE_ARTIFACT_URL + MODEL_FILES_NOT_EXIST_URL;


    public static final String RESOURCE_ARTIFACTS_URL =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_TEAM_2;
    public static final String RESOURCE_ARTIFACTS_URL_2 =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String RESOURCE_ARTIFACTS_URL_3 =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL;
    public static final String RESOURCE_ARTIFACTS_URL_4 =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_VERSION;
    public static final String RESOURCE_ARTIFACTS_URL_WITH_CANARY_HOST_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_TEAM_2;
    public static final String RESOURCE_ARTIFACTS_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + RESOURCE_FILES_URL_WITH_TEAM_2;
    public static final String RESOURCE_ARTIFACTS_URL_MISSING_PROTOCOL_1 =
            TEST_ARTIFACT_REGISTRY + RESOURCE_FILES_URL_WITH_TEAM_2;
    public static final String RESOURCE_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1 =
            "https://not.support.com" + RESOURCE_FILES_URL_WITH_TEAM_2;
    public static final String RESOURCE_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
    public static final String RESOURCE_ARTIFACTS_URL_NOT_EXISTS_1 =
            BASE_ARTIFACT_URL + RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM;

    public static final Set<ModelUdt> TEST_MODELS =
            Set.of(ModelUdt.builder()
                           .name("model-1").version("1.0")
                           .url(MODEL_ARTIFACTS_URL)
                           .build(),
                   ModelUdt.builder()
                           .name("model-2").version("2.0")
                           .url(MODEL_ARTIFACTS_URL_2)
                           .build());

    public static final Set<ModelUdt> TEST_MODELS_2 =
            Set.of(ModelUdt.builder()
                           .name("model-1").version("1.0")
                           .url(MODEL_ARTIFACTS_URL_2)
                           .build(),
                   ModelUdt.builder()
                           .name("model-2").version("2.0")
                           .url(MODEL_ARTIFACTS_URL)
                           .build());

    public static final Set<ResourceUdt> TEST_RESOURCES =
            Set.of(ResourceUdt.builder()
                           .name("resource-1").version("1.0")
                           .url(RESOURCE_ARTIFACTS_URL)
                           .build(),
                   ResourceUdt.builder()
                           .name("resource-2").version("2.0")
                           .url(RESOURCE_ARTIFACTS_URL_2)
                           .build());

    public static final Set<ArtifactDto> TEST_MODEL_DTOS =
            Set.of(ArtifactDto.builder()
                           .name("model-1").version("1.0")
                           .uri(URI.create(MODEL_ARTIFACTS_URL))
                           .build(),
                   ArtifactDto.builder()
                           .name("model-2").version("2.0")
                           .uri(URI.create(MODEL_ARTIFACTS_URL_2))
                           .build());

    public static final Set<ArtifactDto> TEST_RESOURCE_DTOS =
            Set.of(ArtifactDto.builder()
                           .name("resource-1").version("1.0")
                           .uri(URI.create(RESOURCE_ARTIFACTS_URL))
                           .build(),
                   ArtifactDto.builder()
                           .name("resource-2").version("2.0")
                           .uri(URI.create(RESOURCE_ARTIFACTS_URL_2))
                           .build());

    public static final List<ContainerEnvironmentEntryDto> TEST_CONTAINER_ENVIRONMENT =
            List.of(ContainerEnvironmentEntryDto.builder().key("KEY_1").value("VALUE_1").build(),
                    ContainerEnvironmentEntryDto.builder().key("KEY_2").value("VALUE_2").build(),
                    ContainerEnvironmentEntryDto.builder().key("KEY_3").value("VALUE_3").build());
    public static final Key<String> MD_KEY_AUTHORIZATION = Key.of("authorization",
                                                                  Metadata.ASCII_STRING_MARSHALLER);

    public static final Set<String> TEST_TAGS = Set.of("tag1", "tag2", "tag3");
    public static final String TEST_DESCRIPTION = "test-description";

    private static final JsonNode JSON_SECRET_VALUE = OBJECT_MAPPER.createObjectNode()
            .put("AWS_REGION", "us-west-2")
            .put("AWS_BUCKET", "ov-content")
            .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
            .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
            .put("AWS_SESSION_TOKEN", "ov-content-session-token");
    public static final Set<SecretDto> TEST_SECRETS = Set.of(SecretDto.builder()
                                                                     .name(NGC_API_KEY)
                                                                     .value(new StringNode(
                                                                             "shhh!shhh!"))
                                                                     .build(),
                                                             SecretDto.builder()
                                                                     .name("secret2")
                                                                     .value(JSON_SECRET_VALUE)
                                                                     .build());

    // Telemetry UUIDs are referred in
    // src/test/java/resources/fixtures/nvcf/account-with-telemetries-response.json file.
    public static final UUID TEST_TELEMETRY_LOGS_ID =
            UUID.fromString("4df8cb5b-94e0-4fcc-ac12-ddb8da2f25aa");
    public static final UUID TEST_TELEMETRY_METRICS_ID =
            UUID.fromString("d49695b0-86b1-4a84-9b82-2ea823f51d78");
    public static final UUID TEST_TELEMETRY_TRACES_ID =
            UUID.fromString("edc4cd0a-80cd-4a7b-90d9-706b9eae9b4c");
    public static final String TEST_TELEMETRY_ENDPOINT =
            "http://example-telemetry.test.com/endpoint";
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
    public static final String BASE64_SIDECAR_REGISTRY_CRED = Base64.getEncoder().encodeToString(
            "$oauthtoken:nvapi-stg-test-sidecar-cred".getBytes(
                    StandardCharsets.UTF_8));
    public static final RegistryCredentialDto TEST_NGC_CONTAINER_REGISTRY =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .registryHostname(TEST_CONTAINER_REGISTRY)
                    .secret(SecretDto.builder()
                                    .name("container-registry-cred-for-org1")
                                    .value(new StringNode(BASE64_CONTAINER_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto TEST_NGC_MODEL_REGISTRY =
            RegistryCredentialDto.builder()
                    .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                    .registryHostname(TEST_ARTIFACT_REGISTRY)
                    .secret(SecretDto.builder()
                                    .name("model-registry-cred-for-org1")
                                    .value(new StringNode(BASE64_MODEL_REGISTRY_CRED))
                                    .build())
                    .build();
    public static final RegistryCredentialDto
            TEST_NGC_HELM_REGISTRY = RegistryCredentialDto.builder()
            .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
            .registryHostname(TEST_HELM_REGISTRY)
            .secret(SecretDto.builder()
                            .name("helm-registry-cred-for-org1")
                            .value(new StringNode(BASE64_HELM_REGISTRY_CRED))
                            .build())
            .build();
    public static final String BASE64_ENCODED_DOCKER_CRED = Base64.getEncoder()
            .encodeToString("username:docker-pat-password".getBytes(UTF_8));
    public static final RegistryCredentialDto
            TEST_DOCKER_CONTAINER_REGISTRY = RegistryCredentialDto.builder()
            .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
            .registryHostname("docker.io")
            .secret(SecretDto.builder()
                            .name("cred-for-docker-acct-foo")
                            .value(new StringNode(BASE64_ENCODED_DOCKER_CRED))
                            .build())
            .build();
    public static final String BASE64_ENCODED_ECR_PRIVATE_CRED = Base64.getEncoder()
            .encodeToString("access-key-id-1:secret-access-key-1".getBytes(UTF_8));
    public static final String BASE64_ENCODED_ECR_PUBLIC_CRED = Base64.getEncoder()
            .encodeToString("access-key-id-1:secret-access-key-1".getBytes(UTF_8));
    public static final String BASE64_ENCODED_VOLCENGINE_CRED = Base64.getEncoder()
            .encodeToString("volcengine-access-key-1:volcengine-secret-key-1".getBytes(UTF_8));
    public static final String BASE64_ENCODED_ACR_CRED = Base64.getEncoder()
            .encodeToString("acr-client-id-1:acr-client-secret-1".getBytes(UTF_8));
    public static final String BASE64_ENCODED_HARBOR_CRED = Base64.getEncoder()
            .encodeToString("harbor-robot-account:harbor-robot-password".getBytes(UTF_8));
    public static final String BASE64_ENCODED_ARTIFACTORY_CRED = Base64.getEncoder()
            .encodeToString("artifactory-username:artifactory-password".getBytes(UTF_8));

    public static final String TEST_CUSTOM_REGISTRY_NAME_1 = "custom-1";
    public static final String TEST_CUSTOM_REGISTRY_HOST_NAME_1 = "custom-registry-test-1.com";
    public static final URI TEST_CUSTOM_CONTAINER_IMAGE_WITH_TAG_1 =
            URI.create(TEST_CUSTOM_REGISTRY_HOST_NAME_1 + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_CUSTOM_HELM_CHART_WITH_TAG_1 =
            URI.create("oci://" + TEST_CUSTOM_REGISTRY_HOST_NAME_1 + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME, TEST_VALID_OCI_HELM_CHART_TAG_NAME));
}
