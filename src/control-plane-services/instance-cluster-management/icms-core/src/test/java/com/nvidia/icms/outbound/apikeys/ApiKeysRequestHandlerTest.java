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

import static com.nvidia.icms.util.TestUtil.readFileAsBytes;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.when;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.outbound.exception.ApiKeysException;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationRequest;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResponse;
import java.io.IOException;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ApiKeysRequestHandlerTest {

    private static final String NAMESPACE = "nvcfsis";
    private static final String POLICY_NAME = "apikey.allow";

    @Mock
    private ApiKeysStubService apiKeysStubService;

    private JsonMapper objectMapper;
    private ApiKeysRequestHandler apiKeysRequestHandler;

    @BeforeEach
    public void setup() {
        objectMapper = new JsonMapper();
        apiKeysRequestHandler = new ApiKeysRequestHandler(apiKeysStubService, objectMapper, NAMESPACE);
    }

    private ApiKeyValidationRequest getDummyApiKeyValidationRequest() {
        return ApiKeyValidationRequest.builder()
                .jsonField("apiKey", "dummy_api_key")
                .build();
    }

    private ApiKeyValidationResult getDummyParsedResponse() {
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
                                .scopes(List.of("dummy-scope"))
                                .build())
                .build();
    }

    @Test
    void getUserAccessDetails_success() throws IOException {
        ApiKeyValidationResponse response = objectMapper.readValue(
                readFileAsBytes("fixtures/apikeys/authorized_response.json"),
                ApiKeyValidationResponse.class);

        when(apiKeysStubService.evaluatePolicy(eq(NAMESPACE), eq(POLICY_NAME),
                                               any(ApiKeyValidationRequest.class)))
                .thenReturn(response);

        ApiKeyValidationResult result = apiKeysRequestHandler.getUserAccessDetails(
                POLICY_NAME, getDummyApiKeyValidationRequest());

        assertEquals("ZP_Whumw18I0fCYSKpMTGIg1Hnlp4FZ2wtqugF93zK0", result.getNcaId());
    }

    @Test
    void getUserAccessDetails_failedResponse() throws IOException {
        ApiKeyValidationResponse response = objectMapper.readValue(
                readFileAsBytes("fixtures/apikeys/unauthorized_response.json"),
                ApiKeyValidationResponse.class);

        when(apiKeysStubService.evaluatePolicy(eq(NAMESPACE), eq(POLICY_NAME), any(
                ApiKeyValidationRequest.class)))
                .thenReturn(response);

        ApiKeyValidationResult
                result = apiKeysRequestHandler.getUserAccessDetails(POLICY_NAME, getDummyApiKeyValidationRequest());

        assertEquals(false, result.isAllowed());
    }

    @Test
    void getUserAccessDetails_nullResult_throwsException() {
        ApiKeyValidationResponse response = new ApiKeyValidationResponse();
        response.setNamespace(NAMESPACE);
        response.setRuleName(POLICY_NAME);

        when(apiKeysStubService.evaluatePolicy(eq(NAMESPACE), eq(POLICY_NAME),
                                               any(ApiKeyValidationRequest.class)))
                .thenReturn(response);

        ApiKeysException exception = assertThrows(ApiKeysException.class, () ->
                apiKeysRequestHandler.getUserAccessDetails(POLICY_NAME, getDummyApiKeyValidationRequest()));

        assertEquals("Unexpected response from ApiKeys service. No body returned.",
                     exception.getMessage());
    }

    @Test
    void getUserAccessDetails_invalidResultType_throwsException() {
        ApiKeyValidationResponse response = new ApiKeyValidationResponse();
        response.setNamespace(NAMESPACE);
        response.setRuleName(POLICY_NAME);
        response.setResult("invalid-not-a-map");

        when(apiKeysStubService.evaluatePolicy(eq(NAMESPACE), eq(POLICY_NAME),
                                               any(ApiKeyValidationRequest.class)))
                .thenReturn(response);

        ApiKeysException exception = assertThrows(ApiKeysException.class, () ->
                apiKeysRequestHandler.getUserAccessDetails(POLICY_NAME, getDummyApiKeyValidationRequest()));

        assertEquals("Failed to parse user details.", exception.getMessage());
    }

    @Test
    void getUserAccessDetails_generalException_throwsApiKeysException() {
        when(apiKeysStubService.evaluatePolicy(eq(NAMESPACE), eq(POLICY_NAME),
                                               any(ApiKeyValidationRequest.class)))
                .thenThrow(new RuntimeException("Connection refused"));

        ApiKeysException exception = assertThrows(ApiKeysException.class, () ->
                apiKeysRequestHandler.getUserAccessDetails(POLICY_NAME, getDummyApiKeyValidationRequest()));

        assertEquals("Unknown error from ApiKeys service.", exception.getMessage());
    }
}
