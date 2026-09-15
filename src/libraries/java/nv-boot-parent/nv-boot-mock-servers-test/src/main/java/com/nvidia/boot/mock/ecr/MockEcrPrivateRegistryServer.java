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

package com.nvidia.boot.mock.ecr;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
import static com.github.tomakehurst.wiremock.client.WireMock.matchingJsonPath;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_ECR_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_ECR_HELM_CHART_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_ECR_HELM_CHART_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_ECR_REPOSITORY_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_SERVER_ERROR_ECR_REPOSITORY_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_CONTAINER_IMAGE_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_CONTAINER_IMAGE_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_HELM_CHART_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_HELM_CHART_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ECR_REGISTRY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_VERSION;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;

public class MockEcrPrivateRegistryServer {

    private static final String ECR_TARGET_DESCRIBE_IMAGES =
            "AmazonEC2ContainerRegistry_V20150921.DescribeImages";
    private static final String ECR_TARGET_GET_AUTHORIZATION_TOKEN =
            "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken";
    private static final String ECR_ROOT_PATH = "/";
    private static final String AWS_AMZ_TARGET_HEADER = "X-Amz-Target";
    private static final String AWS_AMZ_JSON_CONTENT_TYPE = "application/x-amz-json-1.1";

    private static final String VALID_DESCRIBE_IMAGES_RESPONSE = """
            {
                "imageDetails": [
                    {
                        "imageDigest": "%s",
                        "imageManifestMediaType": "application/vnd.oci.image.index.v1+json",
                        "imageTags": [
                            "%s"
                        ],
                        "registryId": "%s",
                        "repositoryName": "%s"
                    }
                ]
            }
            """.formatted(TEST_VALID_ECR_CONTAINER_IMAGE_DIGEST, TEST_VALID_ECR_CONTAINER_IMAGE_TAG,
                          TEST_VALID_ECR_REGISTRY_ID,
                          TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME);

    private static final String REPOSITORY_NOT_FOUND_RESPONSE = """
            {
                "__type": "RepositoryNotFoundException",
                "message": "The repository with name '%s' does not exist in the registry with id '%s'"
            }
            """.formatted(TEST_NOT_EXIST_ECR_REPOSITORY_NAME, TEST_VALID_ECR_REGISTRY_ID);

    private static final String ACCESS_DENIED_RESPONSE = """
            {
                "__type": "AccessDeniedException", 
                "message": "User: user-test-1 is not authorized to perform: ecr:DescribeImages on resource: arn:aws:ecr:us-west-2:%s:repository/%s"
            }
            """.formatted(TEST_VALID_ECR_REGISTRY_ID, TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME);

    private static final String IMAGE_TAG_NOT_FOUND_RESPONSE = """
            {
                "__type": "ImageNotFoundException",
                "message": "The image with imageId {imageTag:'%s'} does not exist within the repository with name '%s' in the registry with id '%s'"
            }
            """.formatted(TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_TAG,
                          TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                          TEST_VALID_ECR_REGISTRY_ID);

    private static final String IMAGE_DIGEST_NOT_FOUND_RESPONSE = """
            {
                "__type": "ImageNotFoundException",
                "message": "The image with imageId {imageDigest:'%s'} does not exist within the repository with name '%s' in the registry with id '%s'"
            }
            """.formatted(TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_DIGEST,
                          TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME,
                          TEST_VALID_ECR_REGISTRY_ID);

    private static final String SERVER_ERROR_RESPONSE = """
            {
                "__type": "ServerException",
                "message": "An internal server error occurred"
            }
            """;

    private static final String VALID_HELM_CHART_RESPONSE = """
            {
                "imageDetails": [
                    {
                        "imageDigest": "%s",
                        "imageManifestMediaType": "application/vnd.oci.image.manifest.v1+json",
                        "imageTags": [
                            "%s"
                        ],
                        "registryId": "%s",
                        "repositoryName": "%s"
                    }
                ]
            }
            """.formatted(TEST_VALID_ECR_HELM_CHART_DIGEST, TEST_VALID_HELM_CHART_VERSION,
                          TEST_VALID_ECR_REGISTRY_ID, TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME);

    private static final String HELM_CHART_TAG_NOT_FOUND_RESPONSE = """
            {
                "__type": "ImageNotFoundException",
                "message": "The image with imageId {imageTag:'%s'} does not exist within the repository with name '%s' in the registry with id '%s'"
            }
            """.formatted(TEST_NOT_EXIST_ECR_HELM_CHART_TAG, TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                          TEST_VALID_ECR_REGISTRY_ID);

    private static final String HELM_CHART_DIGEST_NOT_FOUND_RESPONSE = """
            {
                "__type": "ImageNotFoundException",
                "message": "The image with imageId {imageDigest:'%s'} does not exist within the repository with name '%s' in the registry with id '%s'"
            }
            """.formatted(TEST_NOT_EXIST_ECR_HELM_CHART_DIGEST,
                          TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME,
                          TEST_VALID_ECR_REGISTRY_ID);

    private static final String VALID_GET_AUTHORIZATION_TOKEN_RESPONSE = """
            {
                "authorizationData": [
                    {
                        "authorizationToken": "QVdTOmV5SndZWGxzYjJGa0lqb2lZV2RrYldGdWFYVnpJbjA9",
                        "expiresAt": 1735689600.0,
                        "proxyEndpoint": "https://%s.dkr.ecr.us-west-2.amazonaws.com"
                    }
                ]
            }
            """.formatted(TEST_VALID_ECR_REGISTRY_ID);

    private static final String INVALID_CREDENTIALS_RESPONSE = """
            {
                "__type": "InvalidSignatureException",
                "message": "The request signature we calculated does not match the signature you provided"
            }
            """;

    private static WireMockServer ecrPrivateRegistryMockServer;

    @SneakyThrows
    public static void start(String ecrRegistryAPIBaseUrl) {
        stop();
        ecrPrivateRegistryMockServer = new WireMockServer(URI.create(ecrRegistryAPIBaseUrl).getPort());
        ecrPrivateRegistryMockServer.start();

        // GetAuthorizationToken - Valid credentials
        // Checks that Authorization header contains the valid access key ID in the Credential parameter
        // Format: AWS4-HMAC-SHA256 Credential=TEST_VALID_ECR_ACCESS_KEY_ID/...
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_GET_AUTHORIZATION_TOKEN))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withHeader(HttpHeaders.AUTHORIZATION, 
                                             matching(".*Credential=" + TEST_VALID_ECR_ACCESS_KEY_ID + "/.*"))
                                 .withRequestBody(equalTo("{}"))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_GET_AUTHORIZATION_TOKEN_RESPONSE)));

        // GetAuthorizationToken - Invalid credentials
        // Checks that Authorization header contains the invalid access key ID in the Credential parameter
        // Format: AWS4-HMAC-SHA256 Credential=TEST_INVALID_ECR_ACCESS_KEY_ID/...
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_GET_AUTHORIZATION_TOKEN))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withHeader(HttpHeaders.AUTHORIZATION, 
                                             matching(".*Credential=" + TEST_INVALID_ECR_ACCESS_KEY_ID + "/.*"))
                                 .withRequestBody(equalTo("{}"))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(INVALID_CREDENTIALS_RESPONSE)));

        // Server error
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_SERVER_ERROR_ECR_REPOSITORY_NAME)))
                                 .willReturn(aResponse().withStatus(500)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(SERVER_ERROR_RESPONSE)));

        // Permission denied repository
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_PERMISSION_DENIED_ECR_REPOSITORY_NAME)))
                                 .willReturn(aResponse().withStatus(403)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(ACCESS_DENIED_RESPONSE)));

        // Repository not found
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_NOT_EXIST_ECR_REPOSITORY_NAME)))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(REPOSITORY_NOT_FOUND_RESPONSE)));

        // Valid repository and image tag
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageTag",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_TAG)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_DESCRIBE_IMAGES_RESPONSE)));

        // Valid repository and image digest
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageDigest",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_DIGEST)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_DESCRIBE_IMAGES_RESPONSE)));

        // Image tag not found (valid repo but invalid tag)
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageTag",
                                                                   equalTo(TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_TAG)))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(IMAGE_TAG_NOT_FOUND_RESPONSE)));

        // Image not found (valid repo but invalid digest)
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_CONTAINER_IMAGE_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageDigest",
                                                                   equalTo(TEST_NOT_EXIST_ECR_CONTAINER_IMAGE_DIGEST)))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(IMAGE_DIGEST_NOT_FOUND_RESPONSE)));

        // Valid Helm chart with tag
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageTag",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_TAG)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_HELM_CHART_RESPONSE)));

        // Valid Helm chart with digest
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageDigest",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_DIGEST)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_HELM_CHART_RESPONSE)));

        // Helm chart not found with invalid tag
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageTag",
                                                                   equalTo(TEST_NOT_EXIST_ECR_HELM_CHART_TAG)))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(HELM_CHART_TAG_NOT_FOUND_RESPONSE)));

        // Helm chart not found with invalid digest
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(TEST_VALID_ECR_HELM_CHART_REPOSITORY_NAME)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageDigest",
                                                                   equalTo(TEST_NOT_EXIST_ECR_HELM_CHART_DIGEST)))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(
                                                             HELM_CHART_DIGEST_NOT_FOUND_RESPONSE)));
    }

    public static void setCustomResponse(String repositoryName, String imageDigest,
                                         String responseBody) {
        ecrPrivateRegistryMockServer
                .stubFor(post(urlPathEqualTo(ECR_ROOT_PATH))
                                 .withHeader(AWS_AMZ_TARGET_HEADER,
                                             equalTo(ECR_TARGET_DESCRIBE_IMAGES))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(AWS_AMZ_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.repositoryName",
                                                                   equalTo(repositoryName)))
                                 .withRequestBody(matchingJsonPath("$.imageIds[0].imageDigest",
                                                                   equalTo(imageDigest)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 AWS_AMZ_JSON_CONTENT_TYPE)
                                                     .withBody(responseBody)));
    }

    public static void stop() {
        if (ecrPrivateRegistryMockServer != null) {
            ecrPrivateRegistryMockServer.stop();
        }
    }
}
