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
package com.nvidia.nvcf.grpc;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.ArgumentMatchers.nullable;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.configuration.exceptions.InvalidInvocationException;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.proto.ClientInvokeRequest;
import com.nvidia.nvcf.proto.ClientInvokeResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService.FunctionContext;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;

class GrpcInvocationServiceUnitTest {

    private static final UUID FUNCTION_ID = UUID.fromString("cb3c1554-336d-4d35-8be8-092b794de452");
    private static final UUID LLM_VERSION_ID = UUID.fromString("7240abc3-a36a-442c-9bfc-99c969b5deed");
    private static final UUID CLASSIC_VERSION_ID = UUID.fromString("74ae1c01-5920-43a7-a97c-dc64747e8820");
    private static final String NCA_ID = "test-account";

    private final GrpcAuthService authService = mock(GrpcAuthService.class);
    private final AccountService accountService = mock(AccountService.class);
    private final FunctionInvocationValidationService validationService =
            mock(FunctionInvocationValidationService.class);
    private final Authentication authentication = new BearerTokenAuthenticationToken("client-token");
    private final GrpcInvocationService service =
            new GrpcInvocationService(authService, accountService, validationService);

    @BeforeEach
    void authenticate() {
        SecurityContextHolder.getContext().setAuthentication(
                new BearerTokenAuthenticationToken("proxy-token"));
        when(authService.validateBearer(any(BearerTokenAuthenticationToken.class), any(String[].class)))
                .thenReturn(authentication);
        when(accountService.getNcaId(authentication)).thenReturn(NCA_ID);
        when(accountService.getOwnerNcaId(authentication, NCA_ID)).thenReturn("owner-account");
    }

    @AfterEach
    void clearAuthentication() {
        SecurityContextHolder.clearContext();
    }

    @Test
    void getNormalizedInferenceUrl() {
        assertEquals("", GrpcInvocationService.getNormalizedInferenceUrl(""));
        assertEquals("/", GrpcInvocationService.getNormalizedInferenceUrl("/"));
        assertEquals("/v2/infer", GrpcInvocationService.getNormalizedInferenceUrl("v2/infer"));
        assertEquals("/v2/infer", GrpcInvocationService.getNormalizedInferenceUrl("/v2/infer"));
    }

    @ParameterizedTest(name = "versioned={0}, admin={1}")
    @CsvSource({"true, false", "false, false", "true, true", "false, true"})
    void rejectsLlmFunctionsBeforeReturningInvocationMetadata(boolean versioned, boolean admin) {
        allowVersions(List.of(function(FunctionType.LLM, LLM_VERSION_ID)));
        var response = new RecordingObserver();

        var error = assertThrows(InvalidInvocationException.class,
                () -> service.authClientInvocation(request(versioned, admin), response));

        assertEquals(404, error.getStatusCode().value());
        assertEquals(NCA_ID, error.getNcaId());
        assertTrue(error.getBody().getDetail().contains("LLM functions cannot be invoked"));
        assertTrue(response.values.isEmpty());
        assertFalse(response.completed);
    }

    @ParameterizedTest(name = "mixed family={0}")
    @ValueSource(booleans = {false, true})
    void returnsOnlyClassicVersions(boolean mixedFamily) {
        var classic = function(FunctionType.DEFAULT, CLASSIC_VERSION_ID);
        allowVersions(mixedFamily
                ? List.of(function(FunctionType.LLM, LLM_VERSION_ID), classic)
                : List.of(classic));
        var response = new RecordingObserver();

        service.authClientInvocation(request(false, false), response);

        assertTrue(response.completed);
        assertNull(response.error);
        assertEquals(1, response.values.size());
        assertEquals(List.of(CLASSIC_VERSION_ID.toString()), response.values.getFirst()
                .getFunctionVersionsList().stream()
                .map(ClientInvokeResponse.FunctionVersion::getFunctionVersionId)
                .toList());
        assertEquals(NCA_ID, response.values.getFirst().getClientNcaId());
    }

    @Test
    void preservesAccessDeniedWithoutRevealingFunctionType() {
        var denied = new NotFoundException("no access to function");
        when(validationService.lookupAndValidateAccess(
                eq(authentication), eq(NCA_ID), eq(FUNCTION_ID), nullable(UUID.class)))
                .thenThrow(denied);
        var response = new RecordingObserver();

        var error = assertThrows(InvalidInvocationException.class,
                () -> service.authClientInvocation(request(true, false), response));

        assertEquals(404, error.getStatusCode().value());
        assertSame(denied, error.getCause());
        assertEquals("no access to function", error.getBody().getDetail());
        assertTrue(response.values.isEmpty());
    }

    private void allowVersions(List<FunctionContext> functions) {
        when(validationService.lookupAndValidateAccess(
                eq(authentication), eq(NCA_ID), eq(FUNCTION_ID), nullable(UUID.class)))
                .thenReturn(functions);
    }

    private static FunctionContext function(FunctionType type, UUID versionId) {
        var function = new FunctionEntity();
        function.setFunctionId(FUNCTION_ID);
        function.setFunctionVersionId(versionId);
        function.setFunctionType(type);
        function.setInferenceUrl("/infer");
        return new FunctionContext("test-subject", NCA_ID, function);
    }

    private static ClientInvokeRequest request(boolean versioned, boolean admin) {
        var request = ClientInvokeRequest.newBuilder()
                .setFunctionId(FUNCTION_ID.toString())
                .setClientAuthorizationToken("client-token");
        if (versioned) {
            request.setFunctionVersionId(LLM_VERSION_ID.toString());
        }
        if (admin) {
            request.setTargetNcaId(NCA_ID);
        }
        return request.build();
    }

    private static class RecordingObserver implements StreamObserver<ClientInvokeResponse> {
        private final List<ClientInvokeResponse> values = new ArrayList<>();
        private Throwable error;
        private boolean completed;

        @Override
        public void onNext(ClientInvokeResponse value) {
            values.add(value);
        }

        @Override
        public void onError(Throwable failure) {
            error = failure;
        }

        @Override
        public void onCompleted() {
            completed = true;
        }
    }
}
