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

package com.nvidia.boot.mock.volcengine;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
import static com.github.tomakehurst.wiremock.client.WireMock.matchingJsonPath;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HELM_CHART_TYPE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_IMAGE_TYPE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_VOLCENGINE_HELM_CHART_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_VOLCENGINE_IMAGE_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_NOT_EXIST_VOLCENGINE_REPOSITORY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_SERVER_ERROR_VOLCENGINE_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_ACCESS_KEY_ID;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_HELM_CHART_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_HELM_REPOSITORY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_IMAGE_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_NAMESPACE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_VOLCENGINE_REGISTRY;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;

public class MockVolcengineRegistryServer {

    private static final String VOLCENGINE_ROOT_PATH = "/";
    private static final String VOLCENGINE_JSON_CONTENT_TYPE = "application/json";

    // Valid container image response
    private static final String VALID_CONTAINER_IMAGE_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-container-123",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing"
                },
                "Result": {
                    "Registry": "%s",
                    "Namespace": "%s",
                    "Repository": "%s",
                    "Items": [
                        {
                            "Name": "%s",
                            "Type": "%s",
                            "Digest": "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1234",
                            "ImageAttributes": [
                                {
                                    "Architecture": "amd64",
                                    "Os": "linux",
                                    "Digest": "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1234"
                                }
                            ]
                        }
                    ]
                }
            }
            """.formatted(TEST_VALID_VOLCENGINE_REGISTRY, TEST_VALID_VOLCENGINE_NAMESPACE,
                          TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY,
                          TEST_VALID_VOLCENGINE_IMAGE_TAG,
                          TEST_IMAGE_TYPE);

    // Valid helm chart response
    private static final String VALID_HELM_CHART_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-helm-456",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing"
                },
                "Result": {
                    "Registry": "%s",
                    "Namespace": "%s",
                    "Repository": "%s",
                    "Items": [
                        {
                            "Name": "%s",
                            "Type": "%s",
                            "Digest": "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1235",
                            "ChartAttribute": {
                                "ApiVersion": "v2",
                                "Name": "%s",
                                "Version": "%s"
                            }
                        }
                    ]
                }
            }
            """.formatted(TEST_VALID_VOLCENGINE_REGISTRY, TEST_VALID_VOLCENGINE_NAMESPACE,
                          TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                          TEST_VALID_VOLCENGINE_HELM_CHART_TAG,
                          TEST_HELM_CHART_TYPE, TEST_VALID_VOLCENGINE_HELM_REPOSITORY,
                          TEST_VALID_VOLCENGINE_HELM_CHART_TAG);

    // Permission denied response
    private static final String PERMISSION_DENIED_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-error-403",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing",
                    "Error": {
                        "Code": "AccessDenied",
                        "Message": "Access denied to registry '%s'"
                    }
                }
            }
            """.formatted(TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY);

    // Not found response
    private static final String NOT_FOUND_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-error-404",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing",
                    "Error": {
                        "Code": "ResourceNotFound",
                        "Message": "The specified resource was not found"
                    }
                }
            }
            """;

    // Repository not found response
    private static final String REPOSITORY_NOT_FOUND_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-error-repo-404",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing",
                    "Error": {
                        "Code": "NotFound.Repository",
                        "Message": "The specified resource %s not found.",
                        "Data": {
                            "__Message.parameterK": "Repository",
                            "__Message.parameterV": "%s"
                        }
                    }
                }
            }
            """;

    // Empty result response for tag not found (200 with empty items)
    private static final String EMPTY_RESULT_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-empty-result",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing"
                },
                "Result": {
                    "Registry": "%s",
                    "TotalCount": 0,
                    "Namespace": "%s",
                    "Repository": "%s",
                    "Items": [],
                    "PageNumber": 1,
                    "PageSize": 10
                }
            }
            """;

    // Server error response
    private static final String SERVER_ERROR_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-error-500",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing",
                    "Error": {
                        "Code": "InternalError",
                        "Message": "An internal server error occurred"
                    }
                }
            }
            """;

    // Valid GetAuthorizationToken response
    private static final String VALID_GET_AUTHORIZATION_TOKEN_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-auth-token-123",
                    "Action": "GetAuthorizationToken",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing"
                },
                "Result": {
                    "Token": "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0LXVzZXIifQ",
                    "Username": "test-user",
                    "ExpireTime": "2025-12-31T23:59:59Z"
                }
            }
            """;

    // Invalid credentials response for GetAuthorizationToken
    private static final String INVALID_CREDENTIALS_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-request-id-auth-error-401",
                    "Action": "GetAuthorizationToken",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing",
                    "Error": {
                        "Code": "InvalidSignature",
                        "Message": "The request signature we calculated does not match the signature you provided"
                    }
                }
            }
            """;

    private static WireMockServer volcengineArtifactRegistryMockServer;

    @SneakyThrows
    public static void start(String volcengineRegistryAPIBaseUrl) {
        stop();
        volcengineArtifactRegistryMockServer =
                new WireMockServer(URI.create(volcengineRegistryAPIBaseUrl).getPort());
        volcengineArtifactRegistryMockServer.start();

        // GetAuthorizationToken - Valid credentials
        // Checks that Authorization header contains the valid access key ID in the Credential parameter
        // Format: HMAC-SHA256 Credential=TEST_VALID_VOLCENGINE_ACCESS_KEY_ID/...
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("GetAuthorizationToken"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withHeader(HttpHeaders.AUTHORIZATION,
                                             matching(".*Credential=" + TEST_VALID_VOLCENGINE_ACCESS_KEY_ID + "/.*"))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_GET_AUTHORIZATION_TOKEN_RESPONSE)));

        // GetAuthorizationToken - Invalid credentials
        // Checks that Authorization header contains the invalid access key ID in the Credential parameter
        // Format: HMAC-SHA256 Credential=TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID/...
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("GetAuthorizationToken"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withHeader(HttpHeaders.AUTHORIZATION,
                                             matching(".*Credential=" + TEST_INVALID_VOLCENGINE_ACCESS_KEY_ID + "/.*"))
                                 .willReturn(aResponse().withStatus(400)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(INVALID_CREDENTIALS_RESPONSE)));

        // Server error registry
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_SERVER_ERROR_VOLCENGINE_REGISTRY)))
                                 .willReturn(aResponse().withStatus(500)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(SERVER_ERROR_RESPONSE)));

        // Permission denied registry
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_PERMISSION_DENIED_VOLCENGINE_REGISTRY)))
                                 .willReturn(aResponse().withStatus(403)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(PERMISSION_DENIED_RESPONSE)));

        // Repository not found
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Repository",
                                                                   equalTo(TEST_NOT_EXIST_VOLCENGINE_REPOSITORY)))
                                 .willReturn(aResponse().withStatus(404)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(
                                                             REPOSITORY_NOT_FOUND_RESPONSE.formatted(
                                                                     TEST_NOT_EXIST_VOLCENGINE_REPOSITORY,
                                                                     TEST_NOT_EXIST_VOLCENGINE_REPOSITORY))));

        // Valid container image with tag
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_VALID_VOLCENGINE_REGISTRY)))
                                 .withRequestBody(matchingJsonPath("$.Namespace",
                                                                   equalTo(TEST_VALID_VOLCENGINE_NAMESPACE)))
                                 .withRequestBody(matchingJsonPath("$.Repository",
                                                                   equalTo(TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Names[0]",
                                                                   equalTo(TEST_VALID_VOLCENGINE_IMAGE_TAG)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Types[0]",
                                                                   equalTo(TEST_IMAGE_TYPE)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_CONTAINER_IMAGE_RESPONSE)));

        // Non-existent container image tag
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_VALID_VOLCENGINE_REGISTRY)))
                                 .withRequestBody(matchingJsonPath("$.Namespace",
                                                                   equalTo(TEST_VALID_VOLCENGINE_NAMESPACE)))
                                 .withRequestBody(matchingJsonPath("$.Repository",
                                                                   equalTo(TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Names[0]",
                                                                   equalTo(TEST_NOT_EXIST_VOLCENGINE_IMAGE_TAG)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Types[0]",
                                                                   equalTo(TEST_IMAGE_TYPE)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(EMPTY_RESULT_RESPONSE.formatted(
                                                             TEST_VALID_VOLCENGINE_REGISTRY,
                                                             TEST_VALID_VOLCENGINE_NAMESPACE,
                                                             TEST_VALID_VOLCENGINE_CONTAINER_IMAGE_REPOSITORY))));

        // Valid helm chart with tag
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_VALID_VOLCENGINE_REGISTRY)))
                                 .withRequestBody(matchingJsonPath("$.Namespace",
                                                                   equalTo(TEST_VALID_VOLCENGINE_NAMESPACE)))
                                 .withRequestBody(matchingJsonPath("$.Repository",
                                                                   equalTo(TEST_VALID_VOLCENGINE_HELM_REPOSITORY)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Names[0]",
                                                                   equalTo(TEST_VALID_VOLCENGINE_HELM_CHART_TAG)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Types[0]",
                                                                   equalTo(TEST_HELM_CHART_TYPE)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(VALID_HELM_CHART_RESPONSE)));

        // Non-existent helm chart tag
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry",
                                                                   equalTo(TEST_VALID_VOLCENGINE_REGISTRY)))
                                 .withRequestBody(matchingJsonPath("$.Namespace",
                                                                   equalTo(TEST_VALID_VOLCENGINE_NAMESPACE)))
                                 .withRequestBody(matchingJsonPath("$.Repository",
                                                                   equalTo(TEST_VALID_VOLCENGINE_HELM_REPOSITORY)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Names[0]",
                                                                   equalTo(TEST_NOT_EXIST_VOLCENGINE_HELM_CHART_TAG)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Types[0]",
                                                                   equalTo(TEST_HELM_CHART_TYPE)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(EMPTY_RESULT_RESPONSE.formatted(
                                                             TEST_VALID_VOLCENGINE_REGISTRY,
                                                             TEST_VALID_VOLCENGINE_NAMESPACE,
                                                             TEST_VALID_VOLCENGINE_HELM_REPOSITORY))));
    }

    public static void setCustomResponse(String registry, String namespace, String repository,
                                         String type, String tagName, String responseBody) {
        volcengineArtifactRegistryMockServer
                .stubFor(post(urlPathEqualTo(VOLCENGINE_ROOT_PATH))
                                 .withQueryParam("Action", equalTo("ListTags"))
                                 .withQueryParam("Version", equalTo("2022-05-12"))
                                 .withHeader(HttpHeaders.CONTENT_TYPE,
                                             equalTo(VOLCENGINE_JSON_CONTENT_TYPE))
                                 .withRequestBody(matchingJsonPath("$.Registry", equalTo(registry)))
                                 .withRequestBody(
                                         matchingJsonPath("$.Namespace", equalTo(namespace)))
                                 .withRequestBody(
                                         matchingJsonPath("$.Repository", equalTo(repository)))
                                 .withRequestBody(matchingJsonPath("$.Filter.Types[0]",
                                                                   equalTo(type)))
                                 .withRequestBody(
                                         matchingJsonPath("$.Filter.Names[0]", equalTo(tagName)))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 VOLCENGINE_JSON_CONTENT_TYPE)
                                                     .withBody(responseBody)));
    }

    public static void stop() {
        if (volcengineArtifactRegistryMockServer != null) {
            volcengineArtifactRegistryMockServer.stop();
        }
    }
}
