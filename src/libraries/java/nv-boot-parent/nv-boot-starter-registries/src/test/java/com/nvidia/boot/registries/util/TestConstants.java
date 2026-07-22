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

package com.nvidia.boot.registries.util;

import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
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
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NOT_EXIST_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_PERMISSION_DENIED_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_TEAM_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_SECRET_ACCESS_KEY;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_NOT_EXIST_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL_WITH_VERSION;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILE_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_NOT_EXISTS_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL_WITH_TEAM;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
import static com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RecognizedRegistryConfiguration;
import static com.nvidia.boot.registries.util.RegistriesConstants.ACR_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PUBLIC_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.HARBOR_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.TestUtils.registryConfig;
import static java.nio.charset.StandardCharsets.UTF_8;

import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties;
import java.net.URI;
import java.time.Duration;
import java.util.Base64;
import java.util.Map;

public class TestConstants {

    // Test NGC Registry Settings
    public static final String TEST_NGC_CONTAINER_REGISTRY = "stg.nvcr.io";
    public static final String TEST_NGC_CONTAINER_REGISTRY_PROD = "nvcr.io";
    public static final String TEST_NGC_CONTAINER_REGISTRY_CANARY = "canary.nvcr.io";
    public static final String TEST_NGC_HELM_REGISTRY = "helm.stg.ngc.nvidia.com";
    public static final String TEST_NGC_HELM_REGISTRY_PROD = "helm.ngc.nvidia.com";
    public static final String TEST_NGC_HELM_REGISTRY_CANARY = "helm.canary.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY = "api.stg.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY_PROD = "api.ngc.nvidia.com";
    public static final String TEST_NGC_ARTIFACT_REGISTRY_CANARY = "api.canary.ngc.nvidia.com";
    public static final String NGC_REGISTRY_NAME = "NGC Private Registry";
    public static final String TEST_CONTAINER_REGISTRY_HOST_NAME_1 =
            "test-container-registry-host-name-1";
    public static final String TEST_HELM_REGISTRY_HOST_NAME_1 = "test-helm-registry-host-name-1";
    public static final String TEST_MODEL_REGISTRY_HOST_NAME_1 = "test-model-registry-host-name-1";
    public static final String TEST_RESOURCE_REGISTRY_HOST_NAME_1 =
            "test-resource-registry-host-name-1";
    public static final String TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1 =
            "test-recognized-container-registry-key-1";
    public static final String TEST_RECOGNIZED_HELM_REGISTRY_KEY_1 =
            "test-recognized-helm-registry-key-1";
    public static final String TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1 =
            "test-recognized-model-registry-key-1";
    public static final String TEST_RECOGNIZED_RESOURCE_REGISTRY_KEY_1 =
            "test-recognized-resource-registry-key-1";

    // Mock NGC Registry Settings
    public static final String MOCK_NGC_CONTAINER_REGISTRY_URL = "http://localhost:9100";
    public static final String MOCK_NGC_REGISTRY_BASE_URL = "http://localhost:9101";
    public static final String MOCK_NGC_REGISTRY_OAUTH2_BASE_URL = "http://localhost:9102";
    public static final String MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE = "ngc-stg";
    public static final Duration MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT = Duration.ofSeconds(30);
    public static final Duration MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT = Duration.ofSeconds(15);
    public static final Duration MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT = Duration.ofSeconds(15);
    public static final Duration MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT = Duration.ofSeconds(30);

    // Mock Docker Registry Settings
    public static final String TEST_DOCKER_REGISTRY = "docker.io";
    public static final String MOCK_DOCKER_REGISTRY_URL = "http://localhost:9105";
    public static final String MOCK_DOCKER_REGISTRY_OAUTH2_URL = "http://localhost:9106";
    public static final String MOCK_DOCKER_REGISTRY_OAUTH2_GROUP_SCOPE = "pull";
    public static final Duration MOCK_DOCKER_REGISTRY_CLIENT_CALL_TIMEOUT = Duration.ofSeconds(30);

    // Mock ECR Registry Settings
    public static final String TEST_ECR_REGISTRY_GLOBAL_HOST_NAME = "dkr.ecr.amazonaws.com";
    public static final String TEST_ECR_PRIVATE_REGISTRY_HOST_NAME =
            "123456789012.dkr.ecr.us-west-2.amazonaws.com";
    public static final String MOCK_ECR_REGISTRY_API_URL = "http://localhost-ecr:9107";
    public static final Duration MOCK_ECR_REGISTRY_CLIENT_CALL_TIMEOUT = Duration.ofSeconds(30);

    // Mock ECR Public Registry Settings
    public static final String TEST_ECR_PUBLIC_REGISTRY_HOST_NAME = "public.ecr.aws";
    public static final String MOCK_ECR_PUBLIC_REGISTRY_API_URL =
            "http://localhost-ecr-public:9108";
    public static final Duration MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT =
            Duration.ofSeconds(30);

    // Mock Volcengine Registry Settings
    public static final String TEST_VOLCENGINE_REGISTRY_GLOBAL_HOST_NAME = "cr.volces.com";
    public static final String MOCK_VOLCENGINE_REGISTRY_API_URL =
            "http://localhost-volcengine:9109";
    public static final Duration MOCK_VOLCENGINE_REGISTRY_CLIENT_CALL_TIMEOUT =
            Duration.ofSeconds(30);

    // Mock Azure Registry Settings
    public static final String TEST_AZURE_REGISTRY_GLOBAL_HOST_NAME = "azurecr.io";
    public static final String MOCK_AZURE_REGISTRY_URL =
            "http://localhost-acr:9110";
    public static final String MOCK_AZURE_REGISTRY_AUTH_URL = "http://localhost-acr-auth:9111";
    public static final Duration MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT =
            Duration.ofSeconds(30);


    // Mock Harbor Registry Settings
    public static final String MOCK_HARBOR_REGISTRY_URL =
            "http://localhost-harbor:9112";
    public static final String MOCK_HARBOR_REGISTRY_AUTH_URL = "http://localhost-harbor-auth:9113";
    public static final Duration MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT = Duration.ofSeconds(30);

    // Mock Artifactory Registry Settings
    public static final String MOCK_ARTIFACTORY_REGISTRY_URL =
            "http://localhost-jfrog:9114";
    public static final String MOCK_ARTIFACTORY_REGISTRY_AUTH_URL =
            "http://localhost-jfrog-auth:9115";
    public static final Duration MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT =
            Duration.ofSeconds(30);

    public static final RegistryConfigurationProperties TEST_REGISTRY_CONFIG_PROPERTIES;

    static {
        var recognized = new RecognizedRegistryConfiguration();
        recognized.setContainer(Map.ofEntries(
                Map.entry(NGC_PRIVATE_REGISTRY_KEY,
                          registryConfig(TEST_NGC_CONTAINER_REGISTRY)),
                Map.entry(DOCKER_REGISTRY_KEY, registryConfig(TEST_DOCKER_REGISTRY)),
                Map.entry(ECR_PRIVATE_REGISTRY_KEY,
                          registryConfig(TEST_ECR_REGISTRY_GLOBAL_HOST_NAME)),
                Map.entry(ECR_PUBLIC_REGISTRY_KEY,
                          registryConfig(TEST_ECR_PUBLIC_REGISTRY_HOST_NAME)),
                Map.entry(ACR_REGISTRY_KEY, registryConfig(TEST_AZURE_REGISTRY_GLOBAL_HOST_NAME)),
                Map.entry(HARBOR_REGISTRY_KEY, registryConfig(TEST_HARBOR_REGISTRY)),
                Map.entry(ARTIFACTORY_REGISTRY_KEY, registryConfig(TEST_ARTIFACTORY_REGISTRY))));
        recognized.setHelm(Map.ofEntries(
                Map.entry(NGC_PRIVATE_REGISTRY_KEY,
                          registryConfig(TEST_NGC_HELM_REGISTRY)),
                Map.entry(DOCKER_REGISTRY_KEY, registryConfig(TEST_DOCKER_REGISTRY)),
                Map.entry(ECR_PRIVATE_REGISTRY_KEY,
                          registryConfig(TEST_ECR_REGISTRY_GLOBAL_HOST_NAME)),
                Map.entry(ECR_PUBLIC_REGISTRY_KEY,
                          registryConfig(TEST_ECR_PUBLIC_REGISTRY_HOST_NAME)),
                Map.entry(ACR_REGISTRY_KEY, registryConfig(TEST_AZURE_REGISTRY_GLOBAL_HOST_NAME)),
                Map.entry(HARBOR_REGISTRY_KEY, registryConfig(TEST_HARBOR_REGISTRY)),
                Map.entry(ARTIFACTORY_REGISTRY_KEY, registryConfig(TEST_ARTIFACTORY_REGISTRY))));
        recognized.setModel(Map.of(
                NGC_PRIVATE_REGISTRY_KEY,
                registryConfig(TEST_NGC_ARTIFACT_REGISTRY)));
        recognized.setResource(Map.of(
                NGC_PRIVATE_REGISTRY_KEY,
                registryConfig(TEST_NGC_ARTIFACT_REGISTRY)));
        TEST_REGISTRY_CONFIG_PROPERTIES = new RegistryConfigurationProperties();
        TEST_REGISTRY_CONFIG_PROPERTIES.setRecognized(recognized);
    }

    // Mock Credentials
    public static final String MOCK_RAW_NGC_CONTAINER_CRED =
            "$oauthtoken:nvapi-stg-test-container-registry-cred";
    public static final String MOCK_NGC_CONTAINER_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_CONTAINER_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_NGC_HELM_CRED =
            "$oauthtoken:nvapi-stg-test-helm-registry-cred";
    public static final String MOCK_NGC_HELM_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_HELM_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_NGC_MODEL_CRED =
            "$oauthtoken:nvapi-stg-test-model-registry-cred";
    public static final String MOCK_NGC_MODEL_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_MODEL_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_NGC_RESOURCE_CRED =
            "$oauthtoken:nvapi-stg-test-resource-registry-cred";
    public static final String MOCK_NGC_RESOURCE_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_NGC_RESOURCE_CRED.getBytes(UTF_8));
    public static final String MOCK_NGC_API_KEY = "mock-ngc-api-key";
    public static final String MOCK_RAW_DOCKER_CONTAINER_CRED =
            "username:dckr_pat_exmaple_personal_token_secret";
    public static final String MOCK_DOCKER_CONTAINER_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_DOCKER_CONTAINER_CRED.getBytes(UTF_8));
    // ECR Private Credentials - Using constants from NvcfTestConstants
    public static final String MOCK_RAW_ECR_CRED =
            TEST_VALID_ECR_ACCESS_KEY_ID + ":" + TEST_VALID_ECR_SECRET_ACCESS_KEY;
    public static final String MOCK_ECR_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_INVALID_ECR_CRED =
            TEST_INVALID_ECR_ACCESS_KEY_ID + ":" + TEST_INVALID_ECR_SECRET_ACCESS_KEY;
    public static final String MOCK_INVALID_ECR_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_INVALID_ECR_CRED.getBytes(UTF_8));

    // ECR Public Credentials - Using constants from NvcfTestConstants
    public static final String MOCK_RAW_ECR_PUBLIC_CRED =
            TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID + ":" + TEST_VALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
    public static final String MOCK_ECR_PUBLIC_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_ECR_PUBLIC_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_INVALID_ECR_PUBLIC_CRED =
            TEST_INVALID_ECR_PUBLIC_ACCESS_KEY_ID + ":" + TEST_INVALID_ECR_PUBLIC_SECRET_ACCESS_KEY;
    public static final String MOCK_INVALID_ECR_PUBLIC_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_INVALID_ECR_PUBLIC_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_VOLCENGINE_CRED =
            TEST_VALID_VOLCENGINE_ACCESS_KEY_ID + ":" + TEST_VALID_VOLCENGINE_SECRET_ACCESS_KEY;
    public static final String MOCK_VOLCENGINE_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_VOLCENGINE_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_INVALID_VOLCENGINE_CRED =
            TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID + ":" + TEST_INVALID_VOLCENGINE_SECRET_ACCESS_KEY;
    public static final String MOCK_INVALID_VOLCENGINE_REGISTRY_CRED =
            Base64.getEncoder().encodeToString(MOCK_RAW_INVALID_VOLCENGINE_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_ACR_CRED =
            "acr_client_id_1:acr_client_secret_1";
    public static final String MOCK_ACR_CREDENTIALS = Base64.getEncoder()
            .encodeToString(MOCK_RAW_ACR_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_HARBOR_CRED =
            "harbor_robot_account_id_1:harbor_robot_secret_1";
    public static final String MOCK_HARBOR_CREDENTIALS = Base64.getEncoder()
            .encodeToString(MOCK_RAW_HARBOR_CRED.getBytes(UTF_8));
    public static final String MOCK_RAW_ARTIFACTORY_CRED =
            "artifactory_user_name_1:artifactory_password_1";
    public static final String MOCK_ARTIFACTORY_CREDENTIALS = Base64.getEncoder()
            .encodeToString(MOCK_RAW_ARTIFACTORY_CRED.getBytes(UTF_8));

    // Test Helm Charts
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
    public static final URI TEST_HELM_CHART_NOT_EXISTS =
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

    // Test NGC Containers
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
                                                                           TEST_VALID_CONTAINER_NOT_EXIST_TAG));
    public static final URI TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           "latest:latest"));
    public static final URI TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           TEST_VALID_CONTAINER_PERMISSION_DENIED_TAG));
    public static final URI TEST_NGC_CONTAINER_IMAGE_UNKNOWN_ORG =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_UNKNOWN_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME,
                                                                           TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_NGC_CONTAINER_IMAGE_2 =
            URI.create(TEST_NGC_CONTAINER_REGISTRY + "/%s/%s:%s".formatted(TEST_VALID_ORG_NAME,
                                                                           TEST_VALID_CONTAINER_NAME
                                                                                   +
                                                                                   "-2",
                                                                           TEST_VALID_CONTAINER_TAG));
    // Test Models
    public static final String BASE_NGC_ARTIFACT_URL = "https://" + TEST_NGC_ARTIFACT_REGISTRY;
    public static final String BASE_NGC_ARTIFACT_URL_CANARY =
            "https://" + TEST_NGC_ARTIFACT_REGISTRY_CANARY;
    public static final String TEST_NGC_MODEL_URL =
            BASE_NGC_ARTIFACT_URL + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_MODEL_URL_WITH_CANARY_HOST =
            BASE_NGC_ARTIFACT_URL_CANARY + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_MODEL_URL_WITH_VERSIONS_1 =
            BASE_NGC_ARTIFACT_URL + MODEL_FILES_URL_WITH_VERSION;
    public static final String TEST_NGC_MODEL_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_MODEL_URL_MISSING_PROTOCOL_1 =
            TEST_NGC_ARTIFACT_REGISTRY + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_MODEL_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_NGC_ARTIFACT_URL + MODEL_FILE_PERMISSION_DENIED_URL;
    public static final String TEST_NGC_MODEL_URL_NOT_EXISTS =
            BASE_NGC_ARTIFACT_URL + MODEL_FILES_NOT_EXIST_URL;

    // Test Resources
    public static final String TEST_NGC_RESOURCE_URL =
            BASE_NGC_ARTIFACT_URL + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_RESOURCE_URL_WITH_CANARY_HOST =
            BASE_NGC_ARTIFACT_URL_CANARY + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_RESOURCE_URL_UNKNOWN_REGISTRY_1 =
            "https://not-exists" + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_RESOURCE_URL_MISSING_PROTOCOL_1 =
            TEST_NGC_ARTIFACT_REGISTRY + RESOURCE_FILES_URL_WITH_TEAM;
    public static final String TEST_NGC_RESOURCE_URL_PERMISSION_DENIED_REGISTRY_1 =
            BASE_NGC_ARTIFACT_URL + RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
    public static final String TEST_NGC_RESOURCE_URL_NOT_EXISTS_1 =
            BASE_NGC_ARTIFACT_URL + RESOURCE_FILES_NOT_EXISTS_URL;

    public static final URI TEST_CONTAINER_IMAGE_1 = URI
            .create(TEST_CONTAINER_REGISTRY_HOST_NAME_1 + "/%s/%s:%s".formatted(
                    TEST_VALID_ORG_NAME, TEST_VALID_CONTAINER_NAME, TEST_VALID_CONTAINER_TAG));
    public static final URI TEST_HELM_CHART_1 = URI
            .create("https://%s/%s/%s/charts/%s-%s.tgz".formatted(
                    TEST_HELM_REGISTRY_HOST_NAME_1, TEST_VALID_ORG_NAME, TEST_VALID_TEAM_NAME,
                    TEST_VALID_HELM_CHART_NAME, TEST_VALID_HELM_CHART_VERSION));
    public static final String TEST_MODEL_URL_1 = "https://" + TEST_MODEL_REGISTRY_HOST_NAME_1
            + MODEL_FILES_URL_WITH_TEAM;
    public static final String TEST_RESOURCE_URL_1 = "https://" + TEST_RESOURCE_REGISTRY_HOST_NAME_1
            + RESOURCE_FILES_URL_WITH_TEAM;
}