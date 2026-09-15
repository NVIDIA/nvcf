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

package com.nvidia.apikeys.services;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.utils.JsonUtils;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import com.nvidia.apikeys.dto.authz.AuthzResponse;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class AuthzServiceTest {

    private static final String NVCF_SERVICE_ID = "nvidia-cloud-functions-ncp-service-id-aketm";
    private static final String NVCT_SERVICE_ID = "nvidia-cloud-tasks-ncp-service-id-nvcttasks";

    @Spy
    private NakProperties nakPropertiesSpy = NakProperties.builder()
            .ncaId("test-nca-id")
            .build();

    @InjectMocks
    private AuthzService authzService;

    private final JsonMapper jsonMapper = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void evaluatePolicy_shouldReturnValidResponse() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {
                        "policies": [
                            {
                                "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
                                "policy": "test-policy"
                            }
                        ]
                    }
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json,IntrospectionResponse.class);

        // Act
        AuthzResponse.Result response = authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse);

        // Assert
        assertNotNull(response);
        assertEquals("test-nca-id", response.getNcaId());
        assertTrue(response.isAllowed());
        assertNotNull(response.getPolicy());
        assertEquals("test-policy", response.getPolicy().get("policy").asString());
    }

    @Test
    void evaluatePolicy_shouldReturnValidResponseForNvctAudience() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {
                        "policies": [
                            {
                                "aud": "nvidia-cloud-tasks-ncp-service-id-nvcttasks",
                                "policy": "nvct-test-policy"
                            }
                        ]
                    }
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act
        AuthzResponse.Result response =
                authzService.evaluatePolicy(NVCT_SERVICE_ID, introspectionResponse);

        // Assert
        assertNotNull(response);
        assertEquals("test-nca-id", response.getNcaId());
        assertTrue(response.isAllowed());
        assertNotNull(response.getPolicy());
        assertEquals("nvct-test-policy", response.getPolicy().get("policy").asString());
    }

    @Test
    void evaluatePolicy_shouldReturnFirstMatchingPolicy() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {
                        "policies": [
                            {
                                "aud": "different-service-id-1",
                                "policy": "first-policy"
                            },
                            {
                                "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
                                "policy": "second-policy"
                            },
                            {
                                "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
                                "policy": "third-policy"
                            }
                        ]
                    }
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act
        AuthzResponse.Result response = authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse);

        // Assert
        assertNotNull(response);
        assertNotNull(response.getPolicy());
        assertEquals("second-policy", response.getPolicy().get("policy").asString());
    }

    @Test
    void evaluatePolicy_shouldThrowExceptionWhenNoPoliciesFound() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {}
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act & Assert
        RuntimeException exception = assertThrows(RuntimeException.class,
                () -> authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse));
        assertEquals("Found no policies in api keys authorizations", exception.getMessage());
    }

    @Test
    void evaluatePolicy_shouldThrowExceptionWhenPoliciesIsNotArray() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {
                        "policies": "not-an-array"
                    }
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act & Assert
        RuntimeException exception = assertThrows(RuntimeException.class,
                () -> authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse));
        assertEquals("Policies field is not an array", exception.getMessage());
    }

    @Test
    void evaluatePolicy_shouldThrowExceptionWhenNoMatchingPolicyFound() throws Exception {
        // Arrange
        String json = """
                {
                    "authorizations": {
                        "policies": [
                            {
                                "aud": "different-service-id-1",
                                "policy": "first-policy"
                            },
                            {
                                "aud": "another-service-id",
                                "policy": "second-policy"
                            }
                        ]
                    }
                }
                """;

        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act & Assert
        RuntimeException exception = assertThrows(RuntimeException.class,
                () -> authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse));
        assertEquals(
                "Found no matching policy in api keys authorizations for audience: " + NVCF_SERVICE_ID,
                exception.getMessage());
    }

    @Test
    void evaluatePolicy_shouldPickPolicyMatchingAudience() throws Exception {
        // Arrange — single key with one policy per audience (NVCF and NVCT).
        String json = """
                {
                    "authorizations": {
                        "policies": [
                            {
                                "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
                                "policy": "nvcf-policy"
                            },
                            {
                                "aud": "nvidia-cloud-tasks-ncp-service-id-nvcttasks",
                                "policy": "nvct-policy"
                            }
                        ]
                    }
                }
                """;
        var introspectionResponse = jsonMapper.readValue(json, IntrospectionResponse.class);

        // Act — evaluate twice with different audience ids against the same key.
        AuthzResponse.Result nvcfResult =
                authzService.evaluatePolicy(NVCF_SERVICE_ID, introspectionResponse);
        AuthzResponse.Result nvctResult =
                authzService.evaluatePolicy(NVCT_SERVICE_ID, introspectionResponse);

        // Assert — each audience picks its own policy.
        assertEquals("nvcf-policy", nvcfResult.getPolicy().get("policy").asString());
        assertEquals("nvct-policy", nvctResult.getPolicy().get("policy").asString());
    }

}
