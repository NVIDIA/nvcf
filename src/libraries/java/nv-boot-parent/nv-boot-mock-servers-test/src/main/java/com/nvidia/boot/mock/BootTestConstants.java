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

package com.nvidia.boot.mock;

import java.net.URI;

public class BootTestConstants {

    public static final String IMAGE_MEDIA_TYPES =
            // Most common format, which may build from Docker 1.10 or later versions.
            "application/vnd.docker.distribution.manifest.v2+json,"
                    // Use to describe a list of image manifests for different platforms.
                    + "application/vnd.docker.distribution.manifest.list.v2+json,"
                    // Equivalent to Docker's manifest list, used for multi-platform images
                    // in the OCI ecosystem
                    + "application/vnd.oci.image.index.v1+json,"
                    // OCI image specification (v1.0 and v1.1), which may build by podman.
                    + "application/vnd.oci.image.manifest.v1+json";

    // Account Test Variables
    public static final String TEST_VALID_ORG_NAME = "whw3rcpsilnj";
    public static final String TEST_VALID_TEAM_NAME = "jeff";
    public static final String TEST_UNKNOWN_ORG_NAME = "someone-org";
    public static final String TEST_UNKNOWN_TEAM_NAME = "test-unknown-team-name";
    public static final String TEST_VALID_ORG_NAME_INVALID_KEY = "test-valid-org-invalid-key";

    // Model Test Variables
    public static final String TEST_VALID_MODEL_NAME = "playground_llama2_trt_l40g";
    public static final String TEST_VALID_MODEL_NAME_2 = "playground_mixtral_trt_l40g";
    public static final String TEST_VALID_SMALL_MODEL_NAME = "playground_llama2_trt_small";

    // Resource Test Variables
    public static final String TEST_VALID_RESOURCE_NAME = "playground_llama2_trt_l40g";
    public static final String TEST_VALID_RESOURCE_NAME_2 = "playground_mixtral_trt_l40g";

    // Helm Chart Test Variables
    public static final String TEST_VALID_HELM_CHART_NAME = "test-helm-chart";
    public static final String TEST_VALID_HELM_CHART_VERSION = "0.6.0+mr.626af78b";
    public static final String TEST_UNKNOWN_HELM_CHART_VERSION = "0.7.0+mr.626af78b";

    // Container Test Variables
    public static final String TEST_VALID_CONTAINER_NAME = "test-container-image";
    public static final String TEST_VALID_CONTAINER_TAG = "latest";
    public static final String TEST_VALID_CONTAINER_PERMISSION_DENIED_TAG = "permission-denied";
    public static final String TEST_VALID_CONTAINER_NOT_EXIST_TAG = "not-exists";
    public static final String TEST_VALID_CONTAINER_HASH =
            "sha256:d3f9786af0f21490f55299ac0af2f2da871f927865b042def17c63a3699d8d51";

    // Docker Test Variables
    public static final String TEST_VALID_DOCKER_NAMESPACE_NAME = "test-docker-namespace";
    public static final String TEST_VALID_DOCKER_REPO_NAME = "test-docker-repo";
    public static final String TEST_VALID_DOCKER_TAG_NAME = "test-docker-container-tag";
    public static final String TEST_PERMISSION_DENINED_DOCKER_NAMESPACE_NAME =
            "test-docker-container-namespace-no-permission";
    public static final String TEST_NOT_EXIST_DOCKER_TAG_NAME =
            "test-docker-container-tag-not-exists";
    public static final String TEST_DOCKER_CONTAINER_REGISTRY = "docker.io";
    public static final URI TEST_DOCKER_CONTAINER_IMAGE =
            URI.create(TEST_DOCKER_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_DOCKER_NAMESPACE_NAME,
                               TEST_VALID_DOCKER_REPO_NAME,
                               TEST_VALID_DOCKER_TAG_NAME));
    public static final URI TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_DOCKER_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_PERMISSION_DENINED_DOCKER_NAMESPACE_NAME,
                               TEST_VALID_DOCKER_REPO_NAME,
                               TEST_VALID_DOCKER_TAG_NAME));
    public static final URI TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_DOCKER_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_DOCKER_NAMESPACE_NAME,
                               TEST_VALID_DOCKER_REPO_NAME,
                               TEST_NOT_EXIST_DOCKER_TAG_NAME));
    public static final URI TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_DOCKER_CONTAINER_REGISTRY + "/%s/%s@%s"
                    .formatted(TEST_VALID_DOCKER_NAMESPACE_NAME,
                               TEST_VALID_DOCKER_REPO_NAME,
                               TEST_VALID_CONTAINER_HASH));
    public static final URI TEST_DOCKER_HELM_CHART =
            URI.create(TEST_DOCKER_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_DOCKER_NAMESPACE_NAME,
                               TEST_VALID_DOCKER_REPO_NAME,
                               TEST_VALID_HELM_CHART_NAME));

    // ECR Test Variables
    public static final String TEST_VALID_ECR_REGISTRY_ID = "779846807323";
    
    // ECR Private Credentials - Valid (used in TestConstants.MOCK_RAW_ECR_CRED)
    public static final String TEST_VALID_ECR_ACCESS_KEY_ID = "ecr_access_key_id_test_1";
    public static final String TEST_VALID_ECR_SECRET_ACCESS_KEY = "ecr_secret_access_key_test_1";
    
    // ECR Private Credentials - Invalid
    public static final String TEST_INVALID_ECR_ACCESS_KEY_ID = "invalid_ecr_key";
    public static final String TEST_INVALID_ECR_SECRET_ACCESS_KEY = "invalid_ecr_secret";
    
    // ECR Public Credentials - Valid (used in TestConstants.MOCK_RAW_ECR_PUBLIC_CRED)
    public static final String TEST_VALID_ECR_PUBLIC_ACCESS_KEY_ID = "ecr_public_access_key_id_test_1";
    public static final String TEST_VALID_ECR_PUBLIC_SECRET_ACCESS_KEY = "ecr_public_secret_access_key_test_1";
    
    // ECR Public Credentials - Invalid
    public static final String TEST_INVALID_ECR_PUBLIC_ACCESS_KEY_ID = "invalid_ecr_public_key";
    public static final String TEST_INVALID_ECR_PUBLIC_SECRET_ACCESS_KEY = "invalid_ecr_public_secret";
    
    public static final String TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME =
            "project/ecr/test/permission-denied";
    public static final String TEST_SERVER_ERROR_ECR_REPOSITORY_NAME =
            "project/ecr/test/server-error";
    public static final String TEST_NOT_EXIST_ECR_REPOSITORY_NAME = "project/ecr/test/not-exist";
    public static final String TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME =
            "project/ecr/container/test-container-image";
    public static final String TEST_VALID_ECR_CONTAINER_IMAGE_TAG = "0.0.1";
    public static final String TEST_VALID_ECR_CONTAINER_IMAGE_DIGEST =
            "sha256:55ed75bd4a2d86607b1ffd8585eb36f21ca82e306adea3ea441c15a6e0b7e490";
    public static final String TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_TAG =
            "not-exist-container-image-tag";
    public static final String TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_DIGEST =
            "sha256:0000000000000000000000000000000000000000000000000000000000000000";

    public static final String TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME =
            "project/ecr/helm-charts/test-helm-chart";
    public static final String TEST_VALID_ECR_HELM_CHART_TAG = "0.0.2";
    public static final String TEST_VALID_ECR_HELM_CHART_DIGEST =
            "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1234";
    public static final String TEST_NOT_EXIST_ECR_HELM_CHART_TAG = "not-exist-helm-chart-tag";
    public static final String TEST_NOT_EXIST_ECR_HELM_CHART_DIGEST =
            "sha256:1111111111111111111111111111111111111111111111111111111111111111";

    // ECR Registry and URI Constants
    public static final String TEST_ECR_CONTAINER_REGISTRY =
            TEST_VALID_ECR_REGISTRY_ID + ".dkr.ecr.us-west-2.amazonaws.com";
    public static final URI TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_ECR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME,
                               TEST_VALID_ECR_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_ECR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_VALID_ECR_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_ECR_CONTAINER_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_VALID_ECR_CONTAINER_IMAGE_DIGEST));
    public static final URI TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND =
            URI.create(TEST_ECR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND =
            URI.create(TEST_ECR_CONTAINER_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_DIGEST));

    public static final String TEST_ECR_HELM_CHART_REGISTRY_URI_PRE =
            "oci://" + TEST_VALID_ECR_REGISTRY_ID + ".dkr.ecr.us-west-2.amazonaws.com";
    public static final URI TEST_ECR_HELM_CHART_PERMISSION_DENIED =
            URI.create(TEST_ECR_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME,
                               TEST_VALID_ECR_HELM_CHART_TAG));
    public static final URI TEST_ECR_HELM_CHART_WITH_TAG =
            URI.create(TEST_ECR_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                               TEST_VALID_ECR_HELM_CHART_TAG));
    public static final URI TEST_ECR_HELM_CHART_WITH_DIGEST =
            URI.create(TEST_ECR_HELM_CHART_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                               TEST_VALID_ECR_HELM_CHART_DIGEST));
    public static final URI TEST_ECR_HELM_CHART_TAG_NOT_FOUND =
            URI.create(TEST_ECR_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_HELM_CHART_TAG));
    public static final URI TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND =
            URI.create(TEST_ECR_HELM_CHART_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_HELM_CHART_DIGEST));

    // ECR Public Registry and URI Constants
    public static final String TEST_ECR_PUBLIC_Alias = "z9d2w9t5";
    public static final String TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE =
            "public.ecr.aws/" + TEST_ECR_PUBLIC_Alias;
    public static final String TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE =
            "oci://public.ecr.aws/" + TEST_ECR_PUBLIC_Alias;

    public static final String TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_REPOSITORY_NAME =
            "project/ecr-public/container/test-container-image-1";
    public static final String TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_TAG = "1.0.0";
    public static final String TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST =
            "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1235";
    public static final String TEST_NOT_EXIST_ECR_PUBLIC_CONTAINER_IMAGE_TAG =
            "not-exist-public-tag";
    public static final String TEST_NOT_EXIST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST =
            "sha256:2222222222222222222222222222222222222222222222222222222222222222";

    public static final String TEST_VALID_ECR_PUBLIC_HELM_CHART_REPOSITORY_NAME =
            "project/ecr-public/helm/test-helm-chart-1";
    public static final String TEST_VALID_ECR_PUBLIC_HELM_CHART_TAG = "2.0.0";
    public static final String TEST_VALID_ECR_PUBLIC_HELM_CHART_DIGEST =
            "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1236";
    public static final String TEST_NOT_EXIST_ECR_PUBLIC_HELM_CHART_TAG =
            "not-exist-public-helm-tag";
    public static final String TEST_NOT_EXIST_ECR_PUBLIC_HELM_CHART_DIGEST =
            "sha256:3333333333333333333333333333333333333333333333333333333333333333";

    public static final String TEST_PERMISSION_DENIED_ECR_PUBLIC_REPOSITORY_NAME =
            "project/ecr-public/test/permission-denied";
    public static final String TEST_NOT_EXIST_ECR_PUBLIC_REPOSITORY_NAME =
            "project/ecr-public/test/not-exist";

    // ECR Public Container Image URIs
    public static final URI TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST));
    public static final URI TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND =
            URI.create(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_NOT_EXIST_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_PUBLIC_CONTAINER_IMAGE_TAG));
    public static final URI TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND =
            URI.create(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_NOT_EXIST_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST));

    // ECR Public Helm Chart URIs
    public static final URI TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG =
            URI.create(TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_VALID_ECR_PUBLIC_HELM_CHART_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_HELM_CHART_TAG));
    public static final URI TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST =
            URI.create(TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_VALID_ECR_PUBLIC_HELM_CHART_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_HELM_CHART_DIGEST));
    public static final URI TEST_ECR_PUBLIC_HELM_CHART_PERMISSION_DENIED =
            URI.create(TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_VALID_ECR_PUBLIC_HELM_CHART_TAG));
    public static final URI TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND =
            URI.create(TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE + "/%s:%s"
                    .formatted(TEST_NOT_EXIST_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_PUBLIC_HELM_CHART_TAG));
    public static final URI TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND =
            URI.create(TEST_ECR_PUBLIC_HELM_CHART_REGISTRY_URI_PRE + "/%s@%s"
                    .formatted(TEST_NOT_EXIST_ECR_PUBLIC_REPOSITORY_NAME,
                               TEST_NOT_EXIST_ECR_PUBLIC_HELM_CHART_DIGEST));

    // Volcengine Test Variables
    public static final String TEST_IMAGE_TYPE = "Image";
    public static final String TEST_HELM_CHART_TYPE = "Chart";
    public static final String TEST_VALID_VOLCENGINE_REGISTRY = "test-volcengine-registry";
    public static final String TEST_VALID_VOLCENGINE_REGION = "cn-beijing";
    public static final String TEST_VALID_VOLCENGINE_NAMESPACE = "test-volcengine-namespace";
    public static final String TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY =
            "test-volcengine-repository";
    public static final String TEST_VALID_VOLCENGINE_IMAGE_TAG = "test-volcengine-image-tag";
    public static final String TEST_VALID_VOLCENGINE_HELM_REPOSITORY =
            "test-volcengine-repository/test-helm-chart-1";
    public static final String TEST_VALID_VOLCENGINE_HELM_CHART_TAG =
            "test-volcengine-helm-chart-tag";

    public static final String TEST_VALID_VOLCENGINE_ACCESS_KEY_ID = "volcengine_access_key_id_test_1";
    public static final String TEST_VALID_VOLCENGINE_SECRET_ACCESS_KEY = "volcengine_secret_access_key_test_1";
    public static final String TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID = "invalid_volcengine_key";
    public static final String TEST_INVALID_VOLCENGINE_SECRET_ACCESS_KEY = "invalid_volcengine_secret";
    
    public static final String TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY =
            "test-volcengine-registry-no-permission";
    public static final String TEST_NOT_EXIST_VOLCENGINE_IMAGE_TAG =
            "test-volcengine-image-tag-not-exists";
    public static final String TEST_NOT_EXIST_VOLCENGINE_HELM_CHART_TAG =
            "test-volcengine-helm-chart-tag-not-exists";
    public static final String TEST_SERVER_ERROR_VOLCENGINE_REGISTRY =
            "test-volcengine-registry-server-error";
    public static final String TEST_NOT_EXIST_VOLCENGINE_REPOSITORY =
            "test-volcengine-repository-not-exists";

    // Volcengine Registry and URI Constants
    public static final String TEST_VOLCENGINE_CONTAINER_REGISTRY =
            TEST_VALID_VOLCENGINE_REGISTRY + "-" + TEST_VALID_VOLCENGINE_REGION + ".cr.volces.com";

    // Volcengine Container Image URIs
    public static final URI TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_VOLCENGINE_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY,
                               TEST_VALID_VOLCENGINE_IMAGE_TAG));
    public static final URI TEST_VOLCENGINE_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(
                    TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY + "-" + TEST_VALID_VOLCENGINE_REGION
                            + ".cr.volces.com/%s/%s:%s"
                            .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                                       TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY,
                                       TEST_VALID_VOLCENGINE_IMAGE_TAG));
    public static final URI TEST_VOLCENGINE_CONTAINER_IMAGE_TAG_NOT_FOUND =
            URI.create(TEST_VOLCENGINE_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY,
                               TEST_NOT_EXIST_VOLCENGINE_IMAGE_TAG));
    public static final URI TEST_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY_NOT_FOUND =
            URI.create(TEST_VOLCENGINE_CONTAINER_REGISTRY + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_NOT_EXIST_VOLCENGINE_REPOSITORY,
                               TEST_VALID_VOLCENGINE_IMAGE_TAG));
    public static final URI TEST_VOLCENGINE_CONTAINER_IMAGE_SERVER_ERROR =
            URI.create(TEST_SERVER_ERROR_VOLCENGINE_REGISTRY + "-" + TEST_VALID_VOLCENGINE_REGION
                               + ".cr.volces.com/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY,
                               TEST_VALID_VOLCENGINE_IMAGE_TAG));

    // Volcengine Helm Chart URIs
    public static final String TEST_VOLCENGINE_HELM_CHART_REGISTRY_URI_PRE =
            "oci://" + TEST_VOLCENGINE_CONTAINER_REGISTRY;
    public static final URI TEST_VOLCENGINE_HELM_CHART_WITH_TAG =
            URI.create(TEST_VOLCENGINE_HELM_CHART_REGISTRY_URI_PRE + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                               TEST_VALID_VOLCENGINE_HELM_CHART_TAG));
    public static final URI TEST_VOLCENGINE_HELM_CHART_PERMISSION_DENIED =
            URI.create("oci://" + TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY + "-"
                               + TEST_VALID_VOLCENGINE_REGION + ".cr.volces.com/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                               TEST_VALID_VOLCENGINE_HELM_CHART_TAG));
    public static final URI TEST_VOLCENGINE_HELM_CHART_TAG_NOT_FOUND =
            URI.create(TEST_VOLCENGINE_HELM_CHART_REGISTRY_URI_PRE + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                               TEST_NOT_EXIST_VOLCENGINE_HELM_CHART_TAG));
    public static final URI TEST_VOLCENGINE_HELM_CHART_REPOSITORY_NOT_FOUND =
            URI.create(TEST_VOLCENGINE_HELM_CHART_REGISTRY_URI_PRE + "/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_NOT_EXIST_VOLCENGINE_REPOSITORY,
                               TEST_VALID_VOLCENGINE_HELM_CHART_TAG));
    public static final URI TEST_VOLCENGINE_HELM_CHART_SERVER_ERROR =
            URI.create("oci://" + TEST_SERVER_ERROR_VOLCENGINE_REGISTRY + "-"
                               + TEST_VALID_VOLCENGINE_REGION + ".cr.volces.com/%s/%s:%s"
                    .formatted(TEST_VALID_VOLCENGINE_NAMESPACE,
                               TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                               TEST_VALID_VOLCENGINE_HELM_CHART_TAG));

    // Generic OCI Registry Test Variables (for mock servers)
    public static final String TEST_VALID_OCI_IMAGE_NAME = "test-oci-namespace/test-image-1";
    public static final String TEST_VALID_OCI_HELM_CHART_NAME =
            "test-oci-namespace/test-helm-chart-1";
    public static final String TEST_PERMISSION_DENIED_OCI_IMAGE_NAME =
            "test-oci-namespace/test-oci-image-repo-no-permission-1";
    public static final String TEST_PERMISSION_DENIED_OCI_HELM_CHART_NAME =
            "test-oci-namespace/test-oci-helm-chart-repo-no-permission-1";
    public static final String TEST_VALID_OCI_IMAGE_TAG_NAME = "test-oci-image-tag-1";
    public static final String TEST_VALID_OCI_HELM_CHART_TAG_NAME = "test-oci-helm-chart-tag-1";
    public static final String TEST_NOT_EXIST_OCI_IMAGE_TAG_NAME =
            "test-oci-image-tag-not-exists-1";
    public static final String TEST_NOT_EXIST_OCI_HELM_CHART_TAG_NAME =
            "test-oci-helm-chart-tag-not-exists-1";
    public static final String TEST_VALID_OCI_IMAGE_DIGEST =
            "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890";
    public static final String TEST_VALID_OCI_HELM_CHART_DIGEST =
            "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567691";

    public static final String TEST_OCI_IMAGE_MANIFEST_URL_WITH_TAG = "/v2/" +
            TEST_VALID_OCI_IMAGE_NAME +
            "/manifests/" + TEST_VALID_OCI_IMAGE_TAG_NAME;
    public static final String TEST_OCI_IMAGE_MANIFEST_URL_WITH_DIGEST = "/v2/" +
            TEST_VALID_OCI_IMAGE_NAME +
            "/manifests/" + TEST_VALID_OCI_IMAGE_DIGEST;
    public static final String TEST_OCI_IMAGE_MANIFEST_LATEST_URL = "/v2/" +
            TEST_VALID_OCI_IMAGE_NAME +
            "/manifests/latest";
    public static final String TEST_OCI_IMAGE_MANIFEST_PERMISSION_DENIED_URL = "/v2/" +
            TEST_PERMISSION_DENIED_OCI_IMAGE_NAME +
            "/manifests/" + TEST_VALID_OCI_IMAGE_TAG_NAME;
    public static final String TEST_OCI_IMAGE_MANIFEST_NOT_EXISTS_URL = "/v2/" +
            TEST_VALID_OCI_IMAGE_NAME +
            "/manifests/" + TEST_NOT_EXIST_OCI_IMAGE_TAG_NAME;
    public static final String TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_TAG = "/v2/" +
            TEST_VALID_OCI_HELM_CHART_NAME +
            "/manifests/" + TEST_VALID_OCI_HELM_CHART_TAG_NAME;
    public static final String TEST_OCI_HELM_CHART_MANIFEST_URL_WITH_DIGEST = "/v2/" +
            TEST_VALID_OCI_HELM_CHART_NAME +
            "/manifests/" + TEST_VALID_OCI_HELM_CHART_DIGEST;
    public static final String TEST_OCI_HELM_CHART_MANIFEST_PERMISSION_DENIED_URL = "/v2/" +
            TEST_PERMISSION_DENIED_OCI_HELM_CHART_NAME +
            "/manifests/" + TEST_VALID_OCI_HELM_CHART_TAG_NAME;
    public static final String TEST_OCI_HELM_CHART_MANIFEST_NOT_EXISTS_URL = "/v2/" +
            TEST_VALID_OCI_HELM_CHART_NAME +
            "/manifests/" + TEST_NOT_EXIST_OCI_HELM_CHART_TAG_NAME;

    // Azure Container Registry (ACR) Test Variables
    public static final String TEST_VALID_ACR_REGISTRY_NAME = "test1-bmfvajfxgfcrhba5";
    public static final String TEST_ACR_CONTAINER_REGISTRY =
            TEST_VALID_ACR_REGISTRY_NAME + ".azurecr.io";

    public static final URI TEST_ACR_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_ACR_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_ACR_CONTAINER_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_DIGEST));
    public static final URI TEST_ACR_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_IMAGE_NAME,
                               TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_ACR_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_NOT_EXIST_OCI_IMAGE_TAG_NAME));

    public static final URI TEST_ACR_HELM_CHART_WITH_TAG =
            URI.create("oci://" + TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME, TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_ACR_HELM_CHART_WITH_DIGEST =
            URI.create("oci://" + TEST_ACR_CONTAINER_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME, TEST_VALID_OCI_HELM_CHART_DIGEST));
    public static final URI TEST_ACR_HELM_CHART_PERMISSION_DENIED =
            URI.create("oci://" + TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_ACR_HELM_CHART_NOT_EXISTS =
            URI.create("oci://" + TEST_ACR_CONTAINER_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_NOT_EXIST_OCI_HELM_CHART_TAG_NAME));

    // Harbor Container Registry Test Variables
    public static final String TEST_HARBOR_REGISTRY = "demo.goharbor.io";

    public static final URI TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_HARBOR_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_VALID_OCI_IMAGE_DIGEST));
    public static final URI TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_IMAGE_NAME,
                               TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME, TEST_NOT_EXIST_OCI_IMAGE_TAG_NAME));

    // Harbor Helm Registry Test Variables
    public static final URI TEST_HARBOR_HELM_CHART_WITH_TAG =
            URI.create("oci://" + TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_HARBOR_HELM_CHART_WITH_DIGEST =
            URI.create("oci://" + TEST_HARBOR_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_DIGEST));
    public static final URI TEST_HARBOR_HELM_CHART_PERMISSION_DENIED =
            URI.create("oci://" + TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_HARBOR_HELM_CHART_NOT_EXISTS =
            URI.create("oci://" + TEST_HARBOR_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_NOT_EXIST_OCI_HELM_CHART_TAG_NAME));

    // Artifactory Test Variables
    public static final String TEST_VALID_ARTIFACTORY_REGISTRY_NAME = "artifactorytest12345";
    public static final String TEST_ARTIFACTORY_REGISTRY =
            TEST_VALID_ARTIFACTORY_REGISTRY_NAME + ".jfrog.io";

    public static final URI TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_TAG =
            URI.create(TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME,
                               TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_DIGEST =
            URI.create(TEST_ARTIFACTORY_REGISTRY + "/%s@%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME,
                               TEST_VALID_OCI_IMAGE_DIGEST));
    public static final URI TEST_ARTIFACTORY_CONTAINER_IMAGE_PERMISSION_DENIED =
            URI.create(TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_IMAGE_NAME,
                               TEST_VALID_OCI_IMAGE_TAG_NAME));
    public static final URI TEST_ARTIFACTORY_CONTAINER_IMAGE_NOT_EXISTS =
            URI.create(TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_IMAGE_NAME,
                               TEST_NOT_EXIST_OCI_IMAGE_TAG_NAME));

    public static final URI TEST_ARTIFACTORY_HELM_CHART_WITH_TAG =
            URI.create("oci://" + TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_ARTIFACTORY_HELM_CHART_WITH_DIGEST =
            URI.create("oci://" + TEST_ARTIFACTORY_REGISTRY + "/%s@%s"
                    .formatted( TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_DIGEST));
    public static final URI TEST_ARTIFACTORY_HELM_CHART_PERMISSION_DENIED =
            URI.create("oci://" + TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_PERMISSION_DENIED_OCI_HELM_CHART_NAME,
                               TEST_VALID_OCI_HELM_CHART_TAG_NAME));
    public static final URI TEST_ARTIFACTORY_HELM_CHART_NOT_EXISTS =
            URI.create("oci://" + TEST_ARTIFACTORY_REGISTRY + "/%s:%s"
                    .formatted(TEST_VALID_OCI_HELM_CHART_NAME,
                               TEST_NOT_EXIST_OCI_HELM_CHART_TAG_NAME));
}
