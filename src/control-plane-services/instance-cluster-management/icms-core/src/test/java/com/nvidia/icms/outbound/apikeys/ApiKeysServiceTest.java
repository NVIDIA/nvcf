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
package com.nvidia.icms.outbound.apikeys;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.IcmsAuthenticationException;
import com.nvidia.icms.outbound.exception.ApiKeysException;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationRequest;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ApiKeysServiceTest {

    @Mock
    private ApiKeysRequestHandler requestHandler;
    private ApiKeysService apiKeysService;

    @BeforeEach
    public void setup() {
        apiKeysService = new ApiKeysService(requestHandler, "package_name", "policy_name", "apiKey");
    }

    private ApiKeyValidationRequest getApiKeyValidationRequest() {
        return ApiKeyValidationRequest.builder()
                .jsonField("apiKey", "dummy_api_key")
                .build();
    }

    private ApiKeyValidationResult getPolicyResult() {
        return ApiKeyValidationResult.builder()
                .allowed(true)
                .ncaId("dummy_nca_id")
                .ownerId("dummy_owner_id")
                .policy(ApiKeyValidationResult.Policy.builder()
                                .aud("dummy_audience")
                                .product("dummy_product")
                                .resources(List.of(ApiKeyValidationResult.Resource.builder()
                                                           .id("ea946155-893c-43b6-920d-3f4410534a80")
                                                           .type("cluster")
                                                           .build()))
                                .scopes(List.of("nvca-cluster"))
                                .build())
                .build();
    }

    @Test
    void fetchApiKeyValidationResult_success() {
        when(requestHandler.getUserAccessDetails("package_name.policy_name",
                                                 getApiKeyValidationRequest())).thenReturn(getPolicyResult());

        ApiKeyValidationResult
                policyResult = apiKeysService.fetchValidationResult("dummy_api_key");

        assertTrue(policyResult.isAllowed());
    }

    @Test
    void fetchApiKeyValidationResult_withInvalidDetails_throwsException() {
        var dummyPolicy = ApiKeyValidationResult.builder()
                .allowed(false)
                .build();
        when(requestHandler.getUserAccessDetails("package_name.policy_name",
                                                 getApiKeyValidationRequest())).thenReturn(dummyPolicy);

        IcmsAuthenticationException exception =
                assertThrows(IcmsAuthenticationException.class,
                             () -> apiKeysService.fetchValidationResult("dummy_api_key"));

        assertEquals("Invalid authentication token", exception.getMessage());
    }

    @Test
    void fetchApiKeyValidationResult_withError_throwsException() {
        when(requestHandler.getUserAccessDetails("package_name.policy_name",
                                                 getApiKeyValidationRequest())).thenThrow(
                new ApiKeysException("dummy_error"));

        ApiKeysException exception =
                assertThrows(ApiKeysException.class,
                             () -> apiKeysService.fetchValidationResult("dummy_api_key"));

        assertEquals("dummy_error", exception.getMessage());
    }
}
