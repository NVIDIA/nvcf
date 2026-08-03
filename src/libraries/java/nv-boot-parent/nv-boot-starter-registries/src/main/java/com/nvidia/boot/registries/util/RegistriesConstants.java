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

import lombok.experimental.UtilityClass;

@UtilityClass
public final class RegistriesConstants {
    public static final java.util.regex.Pattern CAS_MODELS_VERSIONS_PATH =
            java.util.regex.Pattern.compile(
                    "^https://api(\\.(stg|canary))?\\.ngc\\.nvidia\\.com/v2/org/[a-zA-Z_\\-0-9]+(/team/[a-zA-Z_\\-0-9]+)?/models/.+/versions/.+/files");
    public static final java.util.regex.Pattern CAS_DIRECT_MODELS_REGISTRY_PATH =
            java.util.regex.Pattern.compile(
                    "^https://api(\\.(stg|canary))?\\.ngc\\.nvidia\\.com/v2/org/[a-zA-Z0-9_-]+(/team/[a-zA-Z0-9_-]+)?/models/.+/.+/files");
    public static final java.util.regex.Pattern CAS_RESOURCES_VERSIONS_PATH =
            java.util.regex.Pattern.compile(
                    "^https://api(\\.(stg|canary))?\\.ngc\\.nvidia\\.com/v2/org/[a-zA-Z0-9_-]+(/team/[a-zA-Z0-9_-]+)?/resources/.+/versions/.+/files");
    public static final java.util.regex.Pattern CAS_DIRECT_RESOURCES_REGISTRY_PATH =
            java.util.regex.Pattern.compile(
                    "^https://api(\\.(stg|canary))?\\.ngc\\.nvidia\\.com/v2/org/[a-zA-Z0-9_-]+(/team/[a-zA-Z0-9_-]+)?/resources/.+/.+/files");

    public static final String MESG_ARTIFACT_VALIDATED =
            "Artifact '{}' presence validated";
    public static final String MESG_UNSUPPORTED_HOSTNAME =
            "Unsupported registry registryHostname: %s";
    public static final String MESG_ARTIFACT_VALIDATION_FAILED =
            "Artifact '{}' presence validation failed: {}";
    public static final String MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON =
            "Artifact '%s' presence validation failed for unknown reason";
    public static final String MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED =
            "Registry credentials validation failed: {}";
    public static final String MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED_UNKNOWN_REASON =
            "Registry credentials validation failed for unknown reason";

    public static final String NGC_PRIVATE_REGISTRY_KEY = "ngc";
    public static final String NGC_PRIVATE_REGISTRY_NAME = "NGC Private Registry";
    public static final String NGC_ARTIFACT_REGISTRY_PROD_HOSTNAME = "api.ngc.nvidia.com";
    public static final String NGC_CONTAINER_REGISTRY_PROD_HOSTNAME = "nvcr.io";
    public static final String NGC_HELM_REGISTRY_PROD_HOSTNAME = "helm.ngc.nvidia.com";

    public static final String DOCKER_REGISTRY_KEY = "docker";
    public static final String DOCKER_REGISTRY_NAME = "Docker Hub Registry";

    public static final String ECR_PRIVATE_REGISTRY_KEY = "ecr";
    public static final String ECR_PRIVATE_REGISTRY_NAME = "AWS ECR Private Registry";

    public static final String ECR_PUBLIC_REGISTRY_KEY = "ecr-public";
    public static final String ECR_PUBLIC_REGISTRY_NAME = "AWS ECR Public Registry";

    public static final String VOLCENGINE_REGISTRY_KEY = "volcengine";
    public static final String VOLCENGINE_REGISTRY_NAME = "Volcano Engine Registry";

    public static final String ACR_REGISTRY_KEY = "acr";
    public static final String ACR_REGISTRY_NAME = "Azure Container Registry";

    public static final String HARBOR_REGISTRY_KEY = "harbor";
    public static final String HARBOR_REGISTRY_NAME = "Harbor Registry";

    public static final String ARTIFACTORY_REGISTRY_KEY = "artifactory";
    public static final String ARTIFACTORY_REGISTRY_NAME = "JFrog Artifactory Registry";
}
