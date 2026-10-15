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
package com.nvidia.nvcf.rest.function.invocation;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.configuration.notary.NotaryAuthManagerConfiguration.VALIDITY;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_4;
import static org.assertj.core.api.Assertions.assertThat;

import com.nimbusds.jwt.JWTParser;
import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenRequest;
import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenResponse;
import com.nvidia.nvcf.rest.function.invocation.dto.MultiFunctionsInvocationTokenRequest;
import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import com.nvidia.nvcf.util.MockNotaryServer;
import java.util.List;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;

class InvocationAssertionTokenControllerTest extends BaseFunctionInvocationTest {

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl, nvcfAudience, nvcfAudience);
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
    }


    Stream<Arguments> multiFunctionsInvocationTokenArgs() {
        return Stream.of(
                // Single function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_1)
                                                                        .build()))
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.OK),
                // Multiple functions
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_1)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID_2)
                                                                        .functionVersionId(TEST_VERSION_ID_2)
                                                                        .build())
                                                       )
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.OK),
                // multiple functions with and without version id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_1)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionVersionId(TEST_FUNCTION_ID_2)
                                                                        .build())
                                                       )
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.OK),
                // No function version id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .build()))
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.OK),
                // more than 4 functions
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_1)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_2)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_3)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .functionVersionId(TEST_VERSION_ID_4)
                                                                        .build(),
                                                                BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID_2)
                                                                        .functionVersionId(TEST_VERSION_ID_2)
                                                                        .build())
                                                       )
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // No function info
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // No scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .build()))
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.FORBIDDEN),
                // No JWT
                Arguments.of(null,
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.UNAUTHORIZED),
                // No assertionTokenRequest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             null,
                             HttpStatus.UNSUPPORTED_MEDIA_TYPE),
                // Someone else's auth
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                             List.of(),
                                                             100),
                             MultiFunctionsInvocationTokenRequest.builder()
                                     .functions(List.of(BasicFunctionDto
                                                                        .builder()
                                                                        .functionId(TEST_FUNCTION_ID)
                                                                        .build()))
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.FORBIDDEN)
                        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("multiFunctionsInvocationTokenArgs")
    void getMultiFunctionsAssertionToken(String token,
                                         MultiFunctionsInvocationTokenRequest assertionTokenRequest,
                                         HttpStatus expectedStatus) {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var requestEntity = RequestEntity.post("/v2/nvcf/tokens/functions")
                .header("Authorization", "Bearer " + token)
                .body(assertionTokenRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity, InvocationTokenResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        assertThat(responseEntity.getBody()).isNotNull();
        var assertionToken = responseEntity.getBody().accessToken();
        assertThat(assertionToken).isNotNull();
        assertThat(responseEntity.getBody().expiresIn()).isEqualTo(VALIDITY.toSeconds());
        var parsedJwt = JWTParser.parse(assertionToken);
        var claims = parsedJwt.getJWTClaimsSet();
        assertThat(claims.getAudience().getFirst()).isEqualTo(nvcfAudience);
        assertThat(claims.getIssuer()).isEqualTo(notaryBaseUrl);
        var assertions = jsonMapper.convertValue(claims.getClaim("assertion"),
                                                 InvocationAssertion.class);
        assertThat(assertions.ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(assertions.clientId()).isEqualTo("test_client_id");
        assertThat(assertions.intendedFunctions().size()).isEqualTo(assertionTokenRequest.functions().size());
        var functions = assertions.intendedFunctions();
        var requestFunctions = assertionTokenRequest.functions();
        assertThat(requestFunctions).hasSameSizeAs(functions);
        for (var requestFunction : requestFunctions) {
            assertThat(functions).anySatisfy(f -> {
                assertThat(f.functionId()).isEqualTo(requestFunction.functionId());
                assertThat(f.functionVersionId()).isEqualTo(requestFunction.functionVersionId());
            });
        }
    }

    Stream<Arguments> invocationTokenArgs() {
        return Stream.of(
                // Single function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             InvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.OK),
                // No scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             InvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.FORBIDDEN),
                // No JWT
                Arguments.of(null,
                             InvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.UNAUTHORIZED),
                // No assertionTokenRequest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_INVOKE_FUNCTION),
                                                             100),
                             null,
                             HttpStatus.UNSUPPORTED_MEDIA_TYPE),
                // Someone else's auth
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                             List.of(),
                                                             100),
                             InvocationTokenRequest.builder()
                                     .clientId("test_client_id")
                                     .build(),
                             HttpStatus.FORBIDDEN)
                        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("invocationTokenArgs")
    void getAssertionToken(String token, InvocationTokenRequest assertionTokenRequest,
                           HttpStatus expectedStatus) {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var requestEntity = RequestEntity.post("/v2/nvcf/tokens/functions/" + TEST_FUNCTION_ID +
                                                       "/versions/" + TEST_VERSION_ID_1)
                .header("Authorization", "Bearer " + token)
                .body(assertionTokenRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity, InvocationTokenResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        assertThat(responseEntity.getBody()).isNotNull();
        var assertionToken = responseEntity.getBody().accessToken();
        assertThat(assertionToken).isNotNull();
        assertThat(responseEntity.getBody().expiresIn()).isEqualTo(VALIDITY.toSeconds());
        var parsedJwt = JWTParser.parse(assertionToken);
        var claims = parsedJwt.getJWTClaimsSet();
        assertThat(claims.getAudience().getFirst()).isEqualTo(nvcfAudience);
        assertThat(claims.getIssuer()).isEqualTo(notaryBaseUrl);
        var assertions = jsonMapper.convertValue(claims.getClaim("assertion"),
                                                 InvocationAssertion.class);
        assertThat(assertions.ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(assertions.clientId()).isEqualTo("test_client_id");
        assertThat(assertions.intendedFunctions().size()).isEqualTo(1);
        var function = assertions.intendedFunctions().getFirst();
        assertThat(function.functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(function.functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(assertions.ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(assertions.clientId()).isEqualTo("test_client_id");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("invocationTokenArgs")
    void getAssertionTokenWithoutFunctionVersionId(String token,
                                                   InvocationTokenRequest assertionTokenRequest,
                                                   HttpStatus expectedStatus) {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var requestEntity = RequestEntity.post("/v2/nvcf/tokens/functions/" + TEST_FUNCTION_ID)
                .header("Authorization", "Bearer " + token)
                .body(assertionTokenRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity, InvocationTokenResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        assertThat(responseEntity.getBody()).isNotNull();
        var notaryServiceToken = responseEntity.getBody().accessToken();
        assertThat(notaryServiceToken).isNotNull();
        assertThat(responseEntity.getBody().expiresIn()).isEqualTo(VALIDITY.toSeconds());
        var parsedJwt = JWTParser.parse(notaryServiceToken);
        var claims = parsedJwt.getJWTClaimsSet();
        assertThat(claims.getAudience().getFirst()).isEqualTo(nvcfAudience);
        assertThat(claims.getIssuer()).isEqualTo(notaryBaseUrl);
        var assertions = jsonMapper.convertValue(claims.getClaim("assertion"),
                                                 InvocationAssertion.class);
        assertThat(assertions.intendedFunctions().size()).isEqualTo(1);
        var function = assertions.intendedFunctions().getFirst();
        assertThat(function.functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(function.functionVersionId()).isNull();
        assertThat(assertions.ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(assertions.clientId()).isEqualTo("test_client_id");
    }

}

