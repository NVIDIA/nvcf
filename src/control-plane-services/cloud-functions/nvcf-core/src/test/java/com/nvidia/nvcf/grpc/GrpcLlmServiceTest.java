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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType.WORKER;
import static com.nvidia.nvcf.util.MockApiKeysServer.setApiKeyValidationResponse;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.TestConstants.FAKE_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeResponse;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerResponse;
import com.nvidia.nvcf.proto.llm_gateway.LlmGatewayGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.PriorityDto;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.util.TestUtil;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

@Slf4j
class GrpcLlmServiceTest extends BaseFunctionInvocationTest {

    // ---------------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------------

    private AuthLlmInvokeResponse callLlmAuth(String serviceToken, String clientToken, UUID functionId) {
        return callLlmAuth(serviceToken, clientToken, functionId.toString());
    }

    private AuthLlmInvokeResponse callLlmAuth(String serviceToken, String clientToken,
                                              String routingKey) {
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + serviceToken);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        try {
            var stub = LlmGatewayGrpc.newBlockingStub(channel);
            var request = AuthLlmInvokeRequest.newBuilder()
                    .setRoutingKey(routingKey)
                    .setClientAuthorizationToken(clientToken)
                    .build();
            return stub.authLlmInvocation(request);
        } finally {
            channel.shutdownNow();
        }
    }

    private AuthLlmWorkerResponse callLlmWorkerAuth(String serviceToken, String workerToken) {
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + serviceToken);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        try {
            var stub = LlmGatewayGrpc.newBlockingStub(channel);
            var request = AuthLlmWorkerRequest.newBuilder()
                    .setWorkerToken(workerToken)
                    .build();
            return stub.authLlmWorker(request);
        } finally {
            channel.shutdownNow();
        }
    }

    // ---------------------------------------------------------------------------
    // Happy-path tests
    // ---------------------------------------------------------------------------

    @Test
    void authLlmInvocation_happyPath() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionType(TEST_FUNCTION_ID, TEST_VERSION_ID_1, FunctionType.LLM);
        saveFunctionModel(TEST_VERSION_ID_1, "meta/llama-3.1-8b-instruct",
                List.of("/v1/chat/completions", "/v1/responses"), "1-M",
                "meta-llama-tokenizer", "sticky");

        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        var response = callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID);

        assertThat(response).isNotNull();
        assertThat(response.getRoutingKey()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(response.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(response.getAuthContextOrDefault("ncaId", "")).isEqualTo(TEST_NCA_ID);
        assertThat(response.getModelSpecsMap()).containsKey("meta/llama-3.1-8b-instruct");
        var modelSpec = response.getModelSpecsMap().get("meta/llama-3.1-8b-instruct");
        assertThat(modelSpec.getUrisList())
                .containsExactly("/v1/chat/completions", "/v1/responses");
        assertThat(modelSpec.hasTokenRateLimit()).isTrue();
        assertThat(modelSpec.getTokenRateLimit()).isEqualTo("1-M");
        assertThat(modelSpec.hasTokenizer()).isTrue();
        assertThat(modelSpec.getTokenizer()).isEqualTo("meta-llama-tokenizer");
        assertThat(modelSpec.hasRoutingMethod()).isTrue();
        assertThat(modelSpec.getRoutingMethod()).isEqualTo("sticky");
    }

    @Test
    void authLlmInvocation_publicFunction() {
        setFunctionActive(TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        var response = callLlmAuth(serviceToken, clientToken, TEST_PUBLIC_FUNCTION_ID_1);

        assertThat(response).isNotNull();
        assertThat(response.getRoutingKey()).isEqualTo(TEST_PUBLIC_FUNCTION_ID_1.toString());
        assertThat(response.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
    }

    @Test
    void authLlmInvocation_authorizedPartyCanInvoke() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        // TEST_AUTHORIZED_CLIENT_ID_1 belongs to TEST_AUTHORIZED_NCA_ID_1, which is
        // an authorized party on TEST_FUNCTION_ID (set up in BaseFunctionInvocationTest).
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        var response = callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID);

        assertThat(response).isNotNull();
        assertThat(response.getRoutingKey()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(response.getAuthContextOrDefault("ncaId", "")).isEqualTo(TEST_AUTHORIZED_NCA_ID_1);
    }

    @Test
    void authLlmInvocation_apiKeyAuth() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionType(TEST_FUNCTION_ID, TEST_VERSION_ID_1, FunctionType.LLM);
        saveFunctionModel(TEST_VERSION_ID_1, "meta/llama-3.1-70b-instruct",
                List.of(), null);
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("account-functions", "*")),
                    List.of(SCOPE_INVOKE_FUNCTION));
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = "nvapi-stg-some-key";

        var response = callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID);

        assertThat(response).isNotNull();
        assertThat(response.getRoutingKey()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(response.getAuthContextOrDefault("ncaId", "")).isEqualTo(TEST_NCA_ID);
        assertThat(response.getModelSpecsMap()).containsKey("meta/llama-3.1-70b-instruct");
        var modelSpec = response.getModelSpecsMap().get("meta/llama-3.1-70b-instruct");
        assertThat(modelSpec.getUrisList()).isEmpty();
        assertThat(modelSpec.hasTokenRateLimit()).isFalse();
        assertThat(modelSpec.hasTokenizer()).isFalse();
        assertThat(modelSpec.hasRoutingMethod()).isFalse();
    }

    // ---------------------------------------------------------------------------
    // LLM spec field tests
    // ---------------------------------------------------------------------------

    @Test
    void authLlmInvocation_withNoModels_returnsEmptyEndpoints() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionType(TEST_FUNCTION_ID, TEST_VERSION_ID_1, FunctionType.LLM);

        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        var response = callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID);

        assertThat(response.getModelSpecsMap()).isEmpty();
    }

    @Test
    void authLlmWorker_happyPath() {
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_worker");
        var workerToken = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_1, WORKER);

        var response = callLlmWorkerAuth(serviceToken, workerToken);

        assertThat(response).isNotNull();
        assertThat(response.getRoutingKey()).isEqualTo(TEST_FUNCTION_ID.toString());
    }

    @Test
    void authLlmWorker_wrongServiceScope_returnsPermissionDenied() {
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var workerToken = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_1, WORKER);

        assertThatThrownBy(() -> callLlmWorkerAuth(serviceToken, workerToken))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
    }

    @Test
    void authLlmWorker_badWorkerToken_returnsPermissionDenied() {
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_worker");

        assertThatThrownBy(() -> callLlmWorkerAuth(serviceToken, "bad-worker-token"))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
    }

    // ---------------------------------------------------------------------------
    // Auth failure tests
    // ---------------------------------------------------------------------------

    @Test
    void authLlmInvocation_badServiceCredentials_returnsUnauthenticated() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        assertThatThrownBy(() -> callLlmAuth("bad-service-token", clientToken, TEST_FUNCTION_ID))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
    }

    @Test
    void authLlmInvocation_wrongServiceScope_returnsPermissionDenied() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        // Uses invocation scope instead of llm scope.
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("invocation:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        assertThatThrownBy(() -> callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
    }

    @Test
    void authLlmInvocation_badClientCredentials_returnsUnauthenticated() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");

        assertThatThrownBy(() -> callLlmAuth(serviceToken, "bad-client-token", TEST_FUNCTION_ID))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
    }

    @Test
    void authLlmInvocation_clientMissingScope_returnsPermissionDenied() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100);

        assertThatThrownBy(() -> callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
    }

    @Test
    void authLlmInvocation_functionNotFound_returnsNotFound() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        assertThatThrownBy(() -> callLlmAuth(serviceToken, clientToken, FAKE_FUNCTION_ID))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("NOT_FOUND");
    }

    @Test
    void authLlmInvocation_nonUuidRoutingKey_returnsInvalidArgument() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        assertThatThrownBy(() -> callLlmAuth(serviceToken, clientToken, "not-a-uuid"))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("INVALID_ARGUMENT");
    }

    @Test
    void authLlmInvocation_someoneElsesFunction_returnsNotFound() {
        // TEST_FUNCTION_ID_2 belongs to TEST_NCA_ID_2, not TEST_NCA_ID.
        setFunctionActive(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2);
        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);

        assertThatThrownBy(() -> callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID_2))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("NOT_FOUND");
    }

    // ---------------------------------------------------------------------------
    // Parameterized auth matrix
    // ---------------------------------------------------------------------------

    Stream<Arguments> authArgs() {
        return Stream.of(
                // 1. JWT auth for own function - OK
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION), 100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.OK),
                // 2. JWT auth for public function - OK
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION), 100),
                             TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             Status.OK),
                // 3. JWT auth missing scope - PERMISSION_DENIED
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 4. JWT auth for somebody else's function - NOT_FOUND
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION), 100),
                             TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, Status.NOT_FOUND),
                // 5. JWT auth for fake function - NOT_FOUND
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION), 100),
                             FAKE_FUNCTION_ID, UUID.randomUUID(), Status.NOT_FOUND),
                // 6. apikey auth with wildcard account-functions resource - OK
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.OK),
                // 7. apikey auth with function-scoped resource - OK
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.OK),
                // 8. apikey auth missing scope - PERMISSION_DENIED
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of());
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 9. apikey auth wrong resource type - PERMISSION_DENIED
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 10. apikey with bad key (passes nvapi prefix filter) - PERMISSION_DENIED
                Arguments.of((Supplier<String>) () -> {
                    setApiKeyValidationResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                   List.of(new Resource("account-functions", "*")),
                                   List.of(SCOPE_INVOKE_FUNCTION), false);
                    return "nvapi-stg-bad-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 11. No client token - UNKNOWN (null token becomes NPE before reaching grpc)
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1, Status.UNKNOWN)
        );
    }

    @ParameterizedTest
    @MethodSource("authArgs")
    void authLlmInvocation_parameterized(
            Object clientTokenSupplier,
            UUID functionId,
            UUID activeVersionId,
            Status expectedStatus) {
        var clientToken = TestUtil.getToken(clientTokenSupplier);
        setFunctionActive(functionId, activeVersionId);

        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        AuthLlmInvokeResponse response = null;
        Exception exception = null;
        try {
            response = callLlmAuth(serviceToken, clientToken, functionId);
        } catch (Exception e) {
            exception = e;
        }

        if (Status.OK.equals(expectedStatus)) {
            assertThat(response).isNotNull();
            assertThat(response.getRoutingKey()).isEqualTo(functionId.toString());
        } else {
            assertThat(exception).isNotNull();
            assertThat(exception.getClass())
                    .isIn(StatusRuntimeException.class, NullPointerException.class);
            if (exception instanceof StatusRuntimeException sre) {
                assertThat(sre.getStatus().getCode()).isEqualTo(expectedStatus.getCode());
            }
        }
    }

    // ---------------------------------------------------------------------------
    // Priority resolution tests
    // ---------------------------------------------------------------------------

    private void storeLlmConfig(UUID functionVersionId, LlmInvocationConfigDto dto) {
        storeRawLlmConfig(functionVersionId, functionMapperService.toLlmInvocationConfigJson(dto));
    }

    private void storeRawLlmConfig(UUID functionVersionId, String llmConfigJson) {
        var entity = functionsRepository.findAll().stream()
                .filter(f -> functionVersionId.equals(f.getFunctionVersionId()))
                .findFirst()
                .orElseThrow();
        entity.setLlmConfig(llmConfigJson);
        functionsRepository.save(entity);
    }

    private void setFunctionActiveLlm(UUID functionId, UUID functionVersionId) {
        setFunctionActive(functionId, functionVersionId);
        setFunctionType(functionId, functionVersionId, FunctionType.LLM);
    }

    @ParameterizedTest
    @MethodSource("priorityResolutionCases")
    void authLlmInvocation_priorityResolution(
            LlmInvocationConfigDto config, String callerSubject, Long expectedPriority) {
        setFunctionActiveLlm(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        if (config != null) {
            storeLlmConfig(TEST_VERSION_ID_1, config);
        }

        var serviceToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt("llm:check_invocation");
        var clientToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(callerSubject,
                                                          List.of(SCOPE_INVOKE_FUNCTION), 100);
        var response = callLlmAuth(serviceToken, clientToken, TEST_FUNCTION_ID);

        if (expectedPriority == null) {
            assertThat(response.hasPriority()).isFalse();
        } else {
            assertThat(response.hasPriority()).isTrue();
            assertThat(Integer.toUnsignedLong(response.getPriority()))
                    .isEqualTo(expectedPriority.longValue());
        }
    }

    // Each row is (stored llm_config, calling client, expected resolved priority). A null config
    // means nothing is stored; a null expected priority means the response omits the field.
    static Stream<Arguments> priorityResolutionCases() {
        return Stream.of(
                // The caller's per-account override wins over the default.
                Arguments.of(new LlmInvocationConfigDto(new PriorityDto(100L, Map.of(TEST_NCA_ID, 3L))),
                        TEST_CLIENT_SUBJECT, 3L),
                // The caller has no override, so the default applies.
                Arguments.of(new LlmInvocationConfigDto(new PriorityDto(50L, Map.of("nca-other", 1L))),
                        TEST_CLIENT_SUBJECT, 50L),
                // No llm_config is stored, so no priority is returned.
                Arguments.of(null, TEST_CLIENT_SUBJECT, null),
                // The config is present but empty, so no priority is returned.
                Arguments.of(new LlmInvocationConfigDto(null), TEST_CLIENT_SUBJECT, null),
                Arguments.of(
                        new LlmInvocationConfigDto(
                                new PriorityDto(100L, Map.of(TEST_AUTHORIZED_NCA_ID_1, 2L, TEST_NCA_ID, 7L))),
                        TEST_AUTHORIZED_CLIENT_ID_1, 2L),
                Arguments.of(new LlmInvocationConfigDto(new PriorityDto(0L, null)),
                        TEST_CLIENT_SUBJECT, 0L),
                Arguments.of(new LlmInvocationConfigDto(new PriorityDto(4294967295L, null)),
                        TEST_CLIENT_SUBJECT, 4294967295L));
    }
}
