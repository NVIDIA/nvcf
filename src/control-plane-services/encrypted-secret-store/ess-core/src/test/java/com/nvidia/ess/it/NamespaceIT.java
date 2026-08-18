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
package com.nvidia.ess.it;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.controller.response.ListNamespacesResponse;
import com.nvidia.ess.controller.response.NamespaceInfo;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.web.reactive.server.EntityExchangeResult;

@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = EssCoreTestApp.class, webEnvironment = WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active:integration-test",
        })
@ContextConfiguration
@CassandraContainerTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class NamespaceIT extends IntegrationTestsBase {

    private static final String TEST_NS_1 = UUID.randomUUID().toString();
    private static final String TEST_NS_2 = UUID.randomUUID().toString();

    private EntityExchangeResult<ProblemDetail> createNamespaceError(String namespace, String token, HttpStatusCode errorCode) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .bodyValue(buildCreateNamespaceRequest(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> deleteNamespaceError(String namespace, String token, HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/namespaces/" + namespace))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<NamespaceInfo> getNamespace(String namespace) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces/" + namespace))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.OPERATOR))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(NamespaceInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> getNamespaceError(String namespace, String token, HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces/" + namespace))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ListNamespacesResponse> listNamespaces() {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.OPERATOR))
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(ListNamespacesResponse.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> listNamespacesError(String token, HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    @Test
    @Order(1)
    void createNamespace_expiredOperatorToken_unauthorized() {
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    private static Set<TestTokenType> nonOperatorTokenTypes() {

        return Set.of(
            // Valid tenant tokens:
            TestTokenType.NS_ADMIN,
            TestTokenType.ENTITY_ADMIN,
            TestTokenType.SECRET_ADMIN,
            TestTokenType.SECRET_CONSUMER,
            // Expired tenant tokens:
            TestTokenType.NS_ADMIN_EXPIRED,
            TestTokenType.ENTITY_ADMIN_EXPIRED,
            TestTokenType.SECRET_ADMIN_EXPIRED,
            TestTokenType.SECRET_CONSUMER_EXPIRED,
            // Non-ESS JWT to authenticate to the Notary service's /sign endpoint
            // and obtain a Notary token.
            TestTokenType.NOTARY_SIGN_AUTH
        );
    }

    @ParameterizedTest
    @Order(2)
    @MethodSource("nonOperatorTokenTypes")
    void createNamespace_notOperatorToken_forbidden(TestTokenType nonOperatorToken) {
        // Expected return-code is 401 (token not from the expected issuer). Will fix later but keeping this test-case
        // for modifying later.
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(3)
    void createNamespace_legacyAuthTokenFromCorrectIssuer_noScopes_forbidden() {
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(4)
    void createNamespace_legacyAuthTokenFromCorrectIssuer_wrongScope_forbidden() {
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_WRONG_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(5)
    void createNamespace_namespaceDoesNotAlreadyExist_success() {
        var response = verifySuccessResponse(createNamespace(TEST_NS_1), HttpStatus.OK);
        var namespaceInfo = response.getResponseBody();
        assertEquals(TEST_NS_1, namespaceInfo.getNamespace());
        assertNotNull(namespaceInfo.getCreatedAt());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(6)
    void createNamespace_namespaceAlreadyExists_conflict() {
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.CONFLICT), HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(7)
    void getNamespace_expiredOperatorToken_unauthorized() {
        verifyErrorResponse(getNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(8)
    @MethodSource("nonOperatorTokenTypes")
    void getNamespace_notOperatorToken_forbidden(TestTokenType nonOperatorToken) {
        // Expected return-code is 401 (token not from the expected issuer). Will fix later but keeping this test-case
        // for modifying later.
        verifyErrorResponse(getNamespaceError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(9)
    void getNamespace_legacyAuthTokenFromCorrectIssuer_noScopes_forbidden() {
        verifyErrorResponse(getNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(10)
    void getNamespace_legacyAuthTokenFromCorrectIssuer_wrongScope_forbidden() {
        verifyErrorResponse(getNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_WRONG_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(11)
    void getNamespace_namespaceExists_success() {
        var response = verifySuccessResponse(getNamespace(TEST_NS_1), HttpStatus.OK);
        var namespaceInfo = response.getResponseBody();
        assertEquals(TEST_NS_1, namespaceInfo.getNamespace());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(12)
    void getNamespace_namespaceNeverExisted_404() {
        verifyErrorResponse(getNamespaceError(TEST_NS_2, oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(13)
    void listNamespaces_expiredOperatorToken_unauthorized() {
        verifyErrorResponse(listNamespacesError(oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(14)
    @MethodSource("nonOperatorTokenTypes")
    void listNamespaces_notOperatorToken_forbidden(TestTokenType nonOperatorToken) {
        // Expected return-code is 401 (token not from the expected issuer). Will fix later but keeping this test-case
        // for modifying later.
        verifyErrorResponse(listNamespacesError(oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(15)
    void listNamespaces_legacyAuthTokenFromCorrectIssuer_noScopes_forbidden() {
        verifyErrorResponse(listNamespacesError(oauth2Tokens.get(TestTokenType.OPERATOR_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(16)
    void listNamespaces_legacyAuthTokenFromCorrectIssuer_wrongScope_forbidden() {
        verifyErrorResponse(listNamespacesError(oauth2Tokens.get(TestTokenType.OPERATOR_WRONG_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(17)
    void listNamespaces_oneNamespace() {
        var response = verifySuccessResponse(listNamespaces(), HttpStatus.OK);
        var nsList = response.getResponseBody();
        var namespaces = nsList.getNamespaces();
        assertNotNull(namespaces);
        assertEquals(1, namespaces.size());
        assertEquals(TEST_NS_1, namespaces.get(0).getNamespace());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(18)
    void deleteNamespace_expiredOperatorToken_unauthorized() {
        verifyErrorResponse(deleteNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(19)
    @MethodSource("nonOperatorTokenTypes")
    void deleteNamespace_notOperatorToken_forbidden(TestTokenType nonOperatorToken) {
        // Expected return-code is 401 (token not from the expected issuer). Will fix later but keeping this test-case
        // for modifying later.
        verifyErrorResponse(deleteNamespaceError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(20)
    void deleteNamespace_legacyAuthTokenFromCorrectIssuer_noScopes_forbidden() {
        verifyErrorResponse(deleteNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(21)
    void deleteNamespace_legacyAuthTokenFromCorrectIssuer_wrongScope_forbidden() {
        verifyErrorResponse(deleteNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_WRONG_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(22)
    void deleteNamespace_namespaceAlreadyExists_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_1), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(23)
    void deleteNamespace_namespaceAlreadyDeleted_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_1), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(24)
    void deleteNamespace_namespaceNeverExisted_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_2), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(25)
    void getNamespace_namespaceExistedEarlierButWasDeleted_404() {
        verifyErrorResponse(getNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(26)
    void listNamespaces_allNamespacesDeleted_emptyList() {
        var response = verifySuccessResponse(listNamespaces(), HttpStatus.OK);
        var nsList = response.getResponseBody();
        var namespaces = nsList.getNamespaces();
        assertNotNull(namespaces);
        assertTrue(namespaces.isEmpty());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @Test
    @Order(27)
    void createNamespace_namespaceWithSameNameTombstoned_conflict() {
        verifyErrorResponse(createNamespaceError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.CONFLICT), HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }
}
