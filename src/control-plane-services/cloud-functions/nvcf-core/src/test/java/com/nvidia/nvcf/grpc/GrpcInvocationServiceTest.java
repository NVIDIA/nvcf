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
import static com.nvidia.nvcf.util.MockApiKeysServer.setApiKeyValidationResponse;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.FAKE_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.NORMALIZED_TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.proto.ClientInvokeRequest;
import com.nvidia.nvcf.proto.ClientInvokeResponse;
import com.nvidia.nvcf.proto.ClientInvokeResponse.FunctionVersion;
import com.nvidia.nvcf.proto.InvocationGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import com.nvidia.nvcf.util.MockNotaryServer;
import com.nvidia.nvcf.util.NotaryTokenUtils;
import com.nvidia.nvcf.util.TestUtil;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
class GrpcInvocationServiceTest extends BaseFunctionInvocationTest {

    @Value("${nvcf.notary.base-url}")
    private URL notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl.toString(), nvcfAudience, nvcfAudience);
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
    }

    Stream<Arguments> invokeFunctionAdminArgs() {
        return Stream.of(
                // 1. JWT auth for known function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 2. JWT auth for known function accepting any version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, null, TEST_VERSION_ID_1, Status.OK),
                // 3. JWT auth for invoking a public function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1, Status.OK),
                // 4. JWT auth for known function, try to read result without scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 5. JWT auth for known function, try to read result without auth
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 6. JWT auth for known function missing scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1,
                             Status.PERMISSION_DENIED),
                // 7. JWT auth for somebody else's function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_VERSION_ID_2,
                             Status.NOT_FOUND),
                // 8. JWT auth for a fake function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                             100),
                             FAKE_FUNCTION_ID, null, UUID.randomUUID(), Status.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("invokeFunctionAdminArgs")
    void passFunctionAdminAuth(
            Object invokeTokenSupplier,
            UUID functionId,
            @Nullable UUID versionId,
            UUID versionIdActive,
            Status grpcStatus) {
        var invokeToken = TestUtil.getToken(invokeTokenSupplier);
        setFunctionActive(functionId, versionIdActive);
        ClientInvokeResponse clientInvokeResponse = null;
        StatusRuntimeException exception = null;
        String strVersionId = versionId != null ? versionId.toString() : null;
        try {
            clientInvokeResponse =
                    functionAdminAuth(invokeToken, functionId.toString(), strVersionId);
        } catch (StatusRuntimeException e) {
            exception = e;
        }

        if (Status.OK.equals(grpcStatus)) {
            assertThat(clientInvokeResponse).isNotNull();
            assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(functionId.toString());
            assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_ADMIN_SUBJECT);
            assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(clientInvokeResponse.getFunctionVersionsList())
                    .containsExactly(ClientInvokeResponse.FunctionVersion.newBuilder()
                                             .setFunctionVersionId(versionIdActive.toString())
                                             .setDefaultInvocationPath(
                                                     NORMALIZED_TEST_INFERENCE_URL.toString())
                                             .setHasRateLimit(false)
                                             .setSyncCheck(false)
                                             .build());
        } else {
            assertThat(exception).isNotNull();
            assertThat(exception.getStatus().getCode()).isEqualTo(grpcStatus.getCode());
        }
    }

    public Stream<Arguments> invokeFunctionArgs() {
        return Stream.of(
                // 1. JWT auth for known function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 2. JWT auth for known function invoked by the owner accepting any version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, null, TEST_VERSION_ID_1, Status.OK),
                // 3, JWT auth for invoking a public function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1, Status.OK),
                // 4. JWT auth for shared function invoked by an authorized party.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 5. JWT auth for shared function invoked by an authorized party with no OAuth2 Client
                // in the authorized party definition on the function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_2,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 6. JWT auth for known function invoked by an authorized party accepting any version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 7. JWT auth for known function invoked by an authorized party and
                // result fetched by function owner.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 8. JWT auth for known function invoked by the owner and result fetched
                // by authorized party.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 9. JWT auth for known function, someone else tries to read the result
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 10. JWT auth for known function missing scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 11. JWT auth for somebody else's function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                             TEST_VERSION_ID_2, Status.NOT_FOUND),
                // 12. JWT auth for a fake function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             FAKE_FUNCTION_ID, null, UUID.randomUUID(), Status.NOT_FOUND),
                // 13. apikey auth for a known function
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 14. apikey auth for a public function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1, Status.OK),
                // 15. apikey auth for a known function missing correct resource type
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(new Resource(
                                                     "functions", "*")),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 16. apikey auth for a known function missing correct resource type for result lookup
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 17. apikey auth for a known function without calling specific version endpoint
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, null, TEST_VERSION_ID_1, Status.OK),
                // 18. apikey auth for a known function without calling specific version endpoint
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, null, TEST_VERSION_ID_1, Status.OK),
                // 19. apikey auth for a known function
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 20. apikey auth for a known function with more than one permission
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/*"),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*"),
                                        new Resource("some type", "abcd")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 21. bad apikey auth for a known function - missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(new Resource(
                                                     "function", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 22. bad apikey auth for a known function -- missing resource entry
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, null, TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 23. apikey auth for somebody else's function without being an authorized party (no access)
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                             TEST_VERSION_ID_2, Status.NOT_FOUND),
                // 24. apikey auth invoking a function in a different account without being an authorized party
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*")),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                             TEST_VERSION_ID_2, Status.NOT_FOUND),
                // 25. apikey auth for function in a different account without being an authorized party
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID_2, null, TEST_VERSION_ID_2, Status.NOT_FOUND),
                // 26. apikey auth for a fake function
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, FAKE_FUNCTION_ID, null, UUID.randomUUID(), Status.NOT_FOUND),
                // 27. missing auth for a known function
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.UNKNOWN),
                // 28. missing auth for a fake function
                Arguments.of(null, FAKE_FUNCTION_ID, null,
                             UUID.randomUUID(), Status.UNKNOWN),
                // 29. apikey auth for own function
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID_3, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_3 + "/" + TEST_VERSION_ID_3)),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID_3, TEST_VERSION_ID_3, TEST_VERSION_ID_3, Status.OK),
                // 30. apikey auth for a function in different account using authorized party without
                // OAuth2 Client
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_AUTHORIZED_NCA_ID_6, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1, Status.OK),
                // 31. apikey auth for a function in different account without being an authorized party
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_3, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.NOT_FOUND),
                // 32. apikey auth to invoke authorized function by an authorized party but no resource entry
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_6, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_3 + "/"
                                                                          + TEST_VERSION_ID_3)),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.PERMISSION_DENIED),
                // 33. apikey auth for a function in different account without being an authorized party
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_2, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_3 + "/"
                                                                          + TEST_VERSION_ID_3)),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                             TEST_VERSION_ID_3, Status.NOT_FOUND),
                // 34. apikey auth for all the shared functions for an authorized party
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_6, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_INVOKE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             TEST_VERSION_ID_1, Status.OK),
                // 35. apikey auth with bad key (it passes the nvapi prefix filter)
                Arguments.of((Supplier<String>) () -> {
                                 setApiKeyValidationResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                List.of(new Resource("account-functions", "*")),
                                                List.of(SCOPE_INVOKE_FUNCTION), false);
                                 return "nvapi-stg-bad-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_VERSION_ID_1,
                             Status.PERMISSION_DENIED)
        );
    }

    @ParameterizedTest
    @MethodSource("invokeFunctionArgs")
    void passFunctionAuth(
            Object invokeTokenSupplier,
            UUID functionId,
            @Nullable UUID versionId,
            UUID versionIdActive,
            Status grpcStatus) {
        var invokeToken = TestUtil.getToken(invokeTokenSupplier);
        setFunctionActive(functionId, versionIdActive);
        ClientInvokeResponse clientInvokeResponse = null;
        Exception exception = null;
        try {
            clientInvokeResponse = functionAuth(invokeToken, functionId.toString(),
                                                versionId != null ? versionId.toString() : null);
        } catch (Exception e) {
            exception = e;
        }

        if (Status.OK.equals(grpcStatus)) {
            assertThat(clientInvokeResponse).isNotNull();
            assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(functionId.toString());
            assertThat(clientInvokeResponse.getFunctionVersionsList())
                    .containsExactly(ClientInvokeResponse.FunctionVersion.newBuilder()
                                             .setFunctionVersionId(versionIdActive.toString())
                                             .setDefaultInvocationPath(
                                                     NORMALIZED_TEST_INFERENCE_URL.toString())
                                             .setHasRateLimit(false)
                                             .setSyncCheck(false)
                                             .build());
        } else {
            assertThat(exception).isNotNull();
            assertThat(exception.getClass())
                    .isIn(StatusRuntimeException.class, NullPointerException.class);
            if (StatusRuntimeException.class.equals(exception.getClass())) {
                assertThat(((StatusRuntimeException) exception).getStatus().getCode())
                        .isEqualTo(grpcStatus.getCode());
            }
        }
    }

    @Test
    void checkFunctionAuth() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var clientInvokeResponse = functionAuth(
                clientAuth, TEST_FUNCTION_ID.toString(), TEST_VERSION_ID_1.toString());
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setDefaultInvocationPath(
                                                 NORMALIZED_TEST_INFERENCE_URL.toString())
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
    }

    @Test
    void checkFunctionAuthWithRateLimit() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionRateLimit(TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             RateLimitUdt.builder()
                                     .rate("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true).build());
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var clientInvokeResponse = functionAuth(
                clientAuth, TEST_FUNCTION_ID.toString(), TEST_VERSION_ID_1.toString());
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setDefaultInvocationPath(
                                                 NORMALIZED_TEST_INFERENCE_URL.toString())
                                         .setHasRateLimit(true)
                                         .setSyncCheck(true)
                                         .build());
    }

    @Test
    void checkFunctionAuthWithEmptyRateLimit() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionRateLimit(TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             RateLimitUdt.builder().build());
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var clientInvokeResponse = functionAuth(
                clientAuth, TEST_FUNCTION_ID.toString(), TEST_VERSION_ID_1.toString());
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setDefaultInvocationPath(
                                                 NORMALIZED_TEST_INFERENCE_URL.toString())
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
    }

    @Test
    void checkFunctionAdminAuth() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var adminClientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_INVOKE_FUNCTION),
                                                              100);
        var clientInvokeResponse = functionAdminAuth(adminClientAuth, TEST_FUNCTION_ID.toString(),
                                                     TEST_VERSION_ID_1.toString());
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_ADMIN_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setDefaultInvocationPath(
                                                 NORMALIZED_TEST_INFERENCE_URL.toString())
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
    }

    @SneakyThrows
    Stream<Arguments> notaryServiceTokenArgs() {
        return Stream.of(
                // Bad token without a single function nor multi function information
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.PERMISSION_DENIED),

                //.............. Single function token..............
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     TEST_FUNCTION_ID_2,
                                                     TEST_VERSION_ID_2,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Single function without version token
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     TEST_FUNCTION_ID_2,
                                                     null,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Do not own function
                // TEST_NCA_ID only owns TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_FUNCTION_ID_2,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.PERMISSION_DENIED),
                // No client id
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     null),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Expired token
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Token has wrong aud
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             "wrongAudience",
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Token has wrong iss
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             "wrongAudience",
                             URI.create("http://wrongurl").toURL(),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Active public function
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     TEST_PUBLIC_FUNCTION_ID_1,
                                                     TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                                    null,
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             Status.OK),
                // Shared function invoked by an authorized party
                // TEST_AUTHORIZED_NCA_ID_1 is authorized for TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_AUTHORIZED_NCA_ID_1,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_AUTHORIZED_NCA_ID_1,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.OK),
                // Shared function invoked by a non-authorized party
                // TEST_AUTHORIZED_NCA_ID_3 is not authorized for TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_AUTHORIZED_NCA_ID_3,
                                                     TEST_FUNCTION_ID,
                                                     TEST_VERSION_ID_1,
                                                     null,
                                                     "test_client_id"),
                             TEST_AUTHORIZED_NCA_ID_3,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.NOT_FOUND),


                //...............Multi functions token...................
                // TEST_NCA_ID_2 Owns function TEST_FUNCTION_ID_2
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID_2)
                                                             .functionVersionId(TEST_VERSION_ID_2)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // No version id
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID_2)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Multiple functions
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID)
                                                                    .functionVersionId(TEST_VERSION_ID_1)
                                                                    .build(),
                                                             BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID_2)
                                                                     .functionVersionId(TEST_VERSION_ID_2)
                                                                     .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Multiple functions versions
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID_2)
                                                                    .functionVersionId(TEST_VERSION_ID_1)
                                                                    .build(),
                                                            BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID_2)
                                                                    .functionVersionId(TEST_VERSION_ID_2)
                                                                    .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Multiple functions, 1 version id is null
                Arguments.of(new InvocationAssertion(TEST_NCA_ID_2,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID_2)
                                                                    .build(),
                                                            BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID_2)
                                                                    .functionVersionId(TEST_VERSION_ID_2)
                                                                    .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID_2,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             Status.OK),
                // Do not own function
                // TEST_NCA_ID only owns TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID_2)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build(),
                                                            BasicFunctionDto.builder()
                                                                    .functionId(TEST_FUNCTION_ID)
                                                                    .functionVersionId(TEST_VERSION_ID_2)
                                                                    .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.PERMISSION_DENIED),
                // No client id
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     null),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Expired token
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Token has wrong aud
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             "wrongAudience",
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Token has wrong iss
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now().minus(Duration.ofMinutes(20)),
                             "wrongAudience",
                             URI.create("http://wrongurl").toURL(),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.UNAUTHENTICATED),
                // Active public function
                Arguments.of(new InvocationAssertion(TEST_NCA_ID,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_PUBLIC_FUNCTION_ID_1)
                                                             .functionVersionId(TEST_PUBLIC_FUNCTION_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_NCA_ID,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             Status.OK),
                // Shared function invoked by an authorized party
                // TEST_AUTHORIZED_NCA_ID_1 is authorized for TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_AUTHORIZED_NCA_ID_1,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_AUTHORIZED_NCA_ID_1,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.OK),
                // Shared function invoked by a non-authorized party
                // TEST_AUTHORIZED_NCA_ID_3 is not authorized for TEST_FUNCTION_ID, TEST_VERSION_ID_1
                Arguments.of(new InvocationAssertion(TEST_AUTHORIZED_NCA_ID_3,
                                                     null,
                                                     null,
                                                     List.of(BasicFunctionDto.builder()
                                                             .functionId(TEST_FUNCTION_ID)
                                                             .functionVersionId(TEST_VERSION_ID_1)
                                                             .build()),
                                                     "test_client_id"),
                             TEST_AUTHORIZED_NCA_ID_3,
                             TEST_CLIENT_SUBJECT,
                             Instant.now(),
                             nvcfAudience,
                             notaryBaseUrl,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Status.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("notaryServiceTokenArgs")
    @SneakyThrows
    void checkNotaryServiceTokenAuth(
            InvocationAssertion assertion,
            String clientNcaId,
            String subject,
            Instant iat,
            String aud,
            URL iss,
            UUID functionId,
            UUID versionId,
            Status grpcStatus) {
        assertThat(setFunctionActive(functionId, versionId)).isNotNull();
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("invocation:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var invocationStub = InvocationGrpc.newBlockingStub(channel);
        var notaryServiceToken = NotaryTokenUtils.getJwt(subject,
                                                         jsonMapper.writeValueAsString(assertion),
                                                         iss,
                                                         aud,
                                                         Date.from(iat));
        var proxyInvokeRequest = ClientInvokeRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .setClientAuthorizationToken(notaryServiceToken)
                .build();
        ClientInvokeResponse clientInvokeResponse = null;
        Exception exception = null;
        try {
            clientInvokeResponse = invocationStub.authClientInvocation(proxyInvokeRequest);
        } catch (Exception e) {
            exception = e;
        }
        if (Status.OK.equals(grpcStatus)) {
            assertThat(clientInvokeResponse).isNotNull();
            assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(functionId.toString());
            assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(assertion.clientId());
            assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(clientNcaId);
            assertThat(clientInvokeResponse.getFunctionVersionsList())
                    .containsExactly(FunctionVersion.newBuilder()
                                             .setFunctionVersionId(versionId.toString())
                                             .setDefaultInvocationPath(
                                                     NORMALIZED_TEST_INFERENCE_URL.toString())
                                             .setHasRateLimit(false)
                                             .setSyncCheck(false)
                                             .build());
        } else {
            assertThat(exception).isNotNull();
            assertThat(exception.getClass())
                    .isIn(StatusRuntimeException.class, NullPointerException.class);
            if (StatusRuntimeException.class.equals(exception.getClass())) {
                assertThat(((StatusRuntimeException) exception).getStatus().getCode())
                        .isEqualTo(grpcStatus.getCode());
            }
        }
        channel.shutdownNow();
    }

    /**
     * Test that ncaId is included in error metadata when lookupAndValidateAccess fails.
     * This validates the InvalidInvocationException wrapper functionality.
     */
    @Test
    void errorResponseIncludesNcaIdInMetadata() {
        // Use a function that doesn't exist (FAKE_FUNCTION_ID) with valid auth
        // This should trigger a NOT_FOUND error with ncaId in the metadata
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("invocation:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();

        var invocationStub = InvocationGrpc.newBlockingStub(channel);
        var request = ClientInvokeRequest.newBuilder()
                .setFunctionId(FAKE_FUNCTION_ID.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();

        var ncaIdKey = Metadata.Key.of(TAG_NCA_ID, Metadata.ASCII_STRING_MARSHALLER);

        assertThatThrownBy(() -> invocationStub.authClientInvocation(request))
                .isInstanceOf(StatusRuntimeException.class)
                .satisfies(thrown -> {
                    var exception = (StatusRuntimeException) thrown;
                    assertThat(exception.getStatus().getCode()).isEqualTo(Status.NOT_FOUND.getCode());
                    // Verify ncaId is included in the error trailers
                    var trailers = exception.getTrailers();
                    assertThat(trailers).isNotNull();
                    assertThat(trailers.get(ncaIdKey)).isEqualTo(TEST_NCA_ID);
                });

        channel.shutdownNow();
    }

}
