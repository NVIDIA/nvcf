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

import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.ListAuthorizationsResponse;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.TestTemplate;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
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
@WithAuthDbStateVariants
class AuthorizationIT extends IntegrationTestsBase {

    private static final String TEST_NS_1 = UUID.randomUUID().toString();
    private static final String TEST_NS_2 = UUID.randomUUID().toString();

    // Test client identifiers for non-notary authorizations
    private static final String TEST_NON_NOTARY_AUTH_ISS_1 = "https://test-non-notary-issuer-1.example.com";
    private static final String TEST_NON_NOTARY_AUTH_SUB_1 = "test-non-notary-client-1";
    private static final String TEST_NON_NOTARY_AUTH_ISS_2 = "https://test-non-notary-issuer-2.example.com";
    private static final String TEST_NON_NOTARY_AUTH_SUB_2 = "test-non-notary-client-2";

    // Test client identifiers for Notary authorizations
    private static final String TEST_NOTARY_ISS_1 = "https://test-notary-issuer-1.example.com";
    private static final String TEST_NOTARY_SUB_1 = "test-notary-client-1";
    private static final String TEST_NOTARY_ISS_2 = "https://test-notary-issuer-2.example.com";
    private static final String TEST_NOTARY_SUB_2 = "test-notary-client-2";

    // Store created authorization IDs for later tests
    private static String nonNotaryAuthId1;
    private static String nonNotaryAuthId2;
    private static String notaryAuthId1;
    private static String notaryAuthId2;

    // ==================== Helper Methods ====================

    private String nonNotaryAuthorizationsUrl() {
        return "/v1/sys/authorizations/oauth/clients";
    }

    private String nonNotaryAuthorizationUrl(String clientId) {
        return nonNotaryAuthorizationsUrl() + "/" + clientId;
    }

    private EntityExchangeResult<AuthorizationInfo> addNonNotaryAuthorizationSuccess(String namespace, String issuer,
            String subject, String token) {
        var result = webTestClient.post()
                .uri(buildUrl(nonNotaryAuthorizationsUrl()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildCreateAuthorizationRequest(issuer, subject))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AuthorizationInfo.class)
                .returnResult();
        // Force the legacy authorization.type DB column to this variant's value so the read/auth paths
        // below exercise a non-notary record carrying Legacy/OAUTH/random/null type.
        maybeOverwriteAuthTypeInDB(namespace, subject, false);
        return result;
    }

    private EntityExchangeResult<ProblemDetail> addNonNotaryAuthorizationError(String namespace, String issuer,
            String subject, String token, HttpStatusCode errorCode) {
        return webTestClient.post()
                .uri(buildUrl(nonNotaryAuthorizationsUrl()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildCreateAuthorizationRequest(issuer, subject))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<AuthorizationInfo> getNonNotaryAuthorizationSuccess(String namespace, String clientId,
            String token) {
        return webTestClient.get()
                .uri(buildUrl(nonNotaryAuthorizationUrl(clientId)))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AuthorizationInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> getNonNotaryAuthorizationError(String namespace, String clientId,
            String token, HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl(nonNotaryAuthorizationUrl(clientId)))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ListAuthorizationsResponse> listNonNotaryAuthorizationsSuccess(String namespace,
            String token) {
        return webTestClient.get()
                .uri(buildUrl(nonNotaryAuthorizationsUrl()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(ListAuthorizationsResponse.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> listNonNotaryAuthorizationsError(String namespace, String token,
            HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl(nonNotaryAuthorizationsUrl()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<Void> deleteNonNotaryAuthorizationSuccess(String namespace, String clientId, String token) {
        return webTestClient.delete()
                .uri(buildUrl(nonNotaryAuthorizationUrl(clientId)))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isNoContent()
                .expectBody()
                .isEmpty();
    }

    private EntityExchangeResult<ProblemDetail> deleteNonNotaryAuthorizationError(String namespace, String clientId,
            String token, HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl(nonNotaryAuthorizationUrl(clientId)))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<AuthorizationInfo> addNotaryAuthorizationSuccess(String namespace, String issuer,
            String subject, String token) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildCreateAuthorizationRequest(issuer, subject))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AuthorizationInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> addNotaryAuthorizationError(String namespace, String issuer,
            String subject, String token, HttpStatusCode errorCode) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildCreateAuthorizationRequest(issuer, subject))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<AuthorizationInfo> getNotaryAuthorizationSuccess(String namespace, String clientId,
            String token) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients/" + clientId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AuthorizationInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> getNotaryAuthorizationError(String namespace, String clientId,
            String token, HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients/" + clientId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ListAuthorizationsResponse> listNotaryAuthorizationsSuccess(String namespace,
            String token) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(ListAuthorizationsResponse.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> listNotaryAuthorizationsError(String namespace, String token,
            HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<Void> deleteNotaryAuthorizationSuccess(String namespace, String clientId,
            String token) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients/" + clientId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isNoContent()
                .expectBody()
                .isEmpty();
    }

    private EntityExchangeResult<ProblemDetail> deleteNotaryAuthorizationError(String namespace, String clientId,
            String token, HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients/" + clientId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    // ==================== Token Type Providers ====================

    private static Set<TestTokenType> allNonOperatorTokenTypes() {
        return Set.of(
                TestTokenType.NS_ADMIN,
                TestTokenType.ENTITY_ADMIN,
                TestTokenType.SECRET_ADMIN,
                TestTokenType.SECRET_CONSUMER,
                TestTokenType.NS_ADMIN_EXPIRED,
                TestTokenType.ENTITY_ADMIN_EXPIRED,
                TestTokenType.SECRET_ADMIN_EXPIRED,
                TestTokenType.SECRET_CONSUMER_EXPIRED,
                TestTokenType.NOTARY_SIGN_AUTH
        );
    }

    private static Set<TestTokenType> unexpiredNonNsAdminTenantTokenTypes() {
        return Set.of(
                TestTokenType.ENTITY_ADMIN,
                TestTokenType.SECRET_ADMIN,
                TestTokenType.SECRET_CONSUMER
        );
    }

    // Variant-crossed sources: each token value is paired with every AuthApiAndDbStateVariants variant,
    // emitting the variant as the leading argument (consumed by ITExecutionVariantExtension).
    private static Stream<Arguments> allNonOperatorTokenTypesWithVariants() {
        return withAuthDbStateVariants(allNonOperatorTokenTypes());
    }

    private static Stream<Arguments> unexpiredNonNsAdminTenantTokenTypesWithVariants() {
        return withAuthDbStateVariants(unexpiredNonNsAdminTenantTokenTypes());
    }

    // ==================== Non-Notary Authorization Tests - Namespace Does Not Exist ====================

    @ParameterizedTest
    @Order(1)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void addNonNotaryAuthorization_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(2)
    void addNonNotaryAuthorization_expiredOperatorToken_namespaceDoesNotExist_unauthorized() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(3)
    void addNonNotaryAuthorization_operatorTokenNoScopes_namespaceDoesNotExist_forbidden() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(4)
    void addNonNotaryAuthorization_operatorTokenWrongScopes_namespaceDoesNotExist_forbidden() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR_WRONG_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(5)
    void addNonNotaryAuthorization_validOperatorToken_namespaceDoesNotExist_notFound() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(6)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void getNonNotaryAuthorization_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(7)
    void getNonNotaryAuthorization_validOperatorToken_namespaceDoesNotExist_notFound() {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(8)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void listNonNotaryAuthorizations_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(listNonNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(9)
    void listNonNotaryAuthorizations_validOperatorToken_namespaceDoesNotExist_notFound() {
        verifyErrorResponse(listNonNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(10)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void deleteNonNotaryAuthorization_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(deleteNonNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // Notary authorization tests - namespace does not exist

    @ParameterizedTest
    @Order(11)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void addNotaryAuthorization_anyNonOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(12)
    void addNotaryAuthorization_operatorToken_namespaceDoesNotExist_forbidden() {
        // Operator cannot add notary authorizations (only NS-Admin can)
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(13)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void getNotaryAuthorization_anyNonOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(getNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(14)
    void getNotaryAuthorization_operatorToken_namespaceDoesNotExist_forbidden() {
        // Operator cannot get notary authorizations
        verifyErrorResponse(getNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(15)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void listNotaryAuthorizations_anyNonOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(listNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(16)
    void listNotaryAuthorizations_operatorToken_namespaceDoesNotExist_forbidden() {
        // Operator cannot list notary authorizations
        verifyErrorResponse(listNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(17)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void deleteNotaryAuthorization_anyNonOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(deleteNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(18)
    void deleteNotaryAuthorization_operatorToken_namespaceDoesNotExist_forbidden() {
        // Operator cannot delete notary authorizations
        verifyErrorResponse(deleteNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Create Namespace ====================

    @TestTemplate
    @Order(19)
    void createNamespace_success() {
        var response = verifySuccessResponse(createNamespace(TEST_NS_1), HttpStatus.OK);
        assertEquals(effectiveNs(TEST_NS_1), response.getResponseBody().getNamespace());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Non-Notary Authorization Tests - Namespace Exists, No Authorizations ====================

    @ParameterizedTest
    @Order(20)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void addNonNotaryAuthorization_notOperatorToken_namespaceExists_noAuthRegistered_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(21)
    void addNonNotaryAuthorization_operatorToken_namespaceExists_noAuthRegistered_success() {
        var response = verifySuccessResponse(addNonNotaryAuthorizationSuccess(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK);
        assertNotNull(response.getResponseBody().getId());
        assertEquals(TEST_NON_NOTARY_AUTH_ISS_1, response.getResponseBody().getIss());
        nonNotaryAuthId1 = response.getResponseBody().getId();

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Add NS-Admin Authorization ====================

    @TestTemplate
    @Order(22)
    void addNonNotaryAuthorization_registerNsAdmin_success() {
        var response = verifySuccessResponse(addAuthorization(TEST_NS_1, 
                integrationTestProperties.getTenant().getNsAdmin().getIss(),
                integrationTestProperties.getTenant().getNsAdmin().getSub(), true,
                false), HttpStatus.OK);
        assertNotNull(response.getResponseBody().getId());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Non-Notary Authorization Tests - With NS-Admin Registered ====================

    @TestTemplate
    @Order(23)
    void addNonNotaryAuthorization_expiredNsAdminToken_namespaceExists_unauthorized() {
        // Only NS_ADMIN is registered at this point, so only NS_ADMIN_EXPIRED will get 401 (expired)
        // Other expired tokens would get 403 (issuer not registered)
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_2, TEST_NON_NOTARY_AUTH_SUB_2,
                oauth2Tokens.get(TestTokenType.NS_ADMIN_EXPIRED), HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(24)
    @MethodSource("unexpiredNonNsAdminTenantTokenTypesWithVariants")
    void addNonNotaryAuthorization_nonNsAdminTenantToken_namespaceExists_forbidden(ITExecutionVariant variant, TestTokenType nonAdminToken) {
        // Non-NS-Admin tenant tokens don't have the required scope
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_2, TEST_NON_NOTARY_AUTH_SUB_2,
                oauth2Tokens.get(nonAdminToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(25)
    void addNonNotaryAuthorization_nsAdminToken_namespaceExists_success() {
        var response = verifySuccessResponse(addNonNotaryAuthorizationSuccess(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_2, TEST_NON_NOTARY_AUTH_SUB_2,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        assertNotNull(response.getResponseBody().getId());
        assertEquals(TEST_NON_NOTARY_AUTH_ISS_2, response.getResponseBody().getIss());
        nonNotaryAuthId2 = response.getResponseBody().getId();

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(26)
    void addNonNotaryAuthorization_duplicateClient_conflict() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.CONFLICT), HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Get Non-Notary Authorization Tests ====================

    @TestTemplate
    @Order(27)
    void getNonNotaryAuthorization_operatorToken_success() {
        var response = verifySuccessResponse(getNonNotaryAuthorizationSuccess(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK);
        assertEquals(nonNotaryAuthId1, response.getResponseBody().getId());
        assertEquals(TEST_NON_NOTARY_AUTH_ISS_1, response.getResponseBody().getIss());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(28)
    void getNonNotaryAuthorization_nsAdminToken_success() {
        var response = verifySuccessResponse(getNonNotaryAuthorizationSuccess(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        assertEquals(nonNotaryAuthId1, response.getResponseBody().getId());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(29)
    void getNonNotaryAuthorization_nonexistentClient_notFound() {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(30)
    @MethodSource("unexpiredNonNsAdminTenantTokenTypesWithVariants")
    void getNonNotaryAuthorization_nonNsAdminTenantToken_forbidden(ITExecutionVariant variant, TestTokenType nonAdminToken) {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(nonAdminToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== List Non-Notary Authorizations Tests ====================

    @TestTemplate
    @Order(31)
    void listNonNotaryAuthorizations_operatorToken_success() {
        var response = verifySuccessResponse(listNonNotaryAuthorizationsSuccess(TEST_NS_1,
                oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK);
        var authList = response.getResponseBody().getAuthorizations();
        assertNotNull(authList);
        // Should have at least the test authorizations we added plus the NS-Admin
        assertTrue(authList.size() >= 3);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(32)
    void listNonNotaryAuthorizations_nsAdminToken_success() {
        var response = verifySuccessResponse(listNonNotaryAuthorizationsSuccess(TEST_NS_1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        var authList = response.getResponseBody().getAuthorizations();
        assertNotNull(authList);
        assertTrue(authList.size() >= 3);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(33)
    @MethodSource("unexpiredNonNsAdminTenantTokenTypesWithVariants")
    void listNonNotaryAuthorizations_nonNsAdminTenantToken_forbidden(ITExecutionVariant variant, TestTokenType nonAdminToken) {
        verifyErrorResponse(listNonNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(nonAdminToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Notary Authorization Tests - Namespace Exists ====================

    @TestTemplate
    @Order(34)
    void addNotaryAuthorization_operatorToken_namespaceExists_forbidden() {
        // Operator cannot add notary authorizations
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(35)
    @MethodSource("unexpiredNonNsAdminTenantTokenTypesWithVariants")
    void addNotaryAuthorization_nonNsAdminTenantToken_forbidden(ITExecutionVariant variant, TestTokenType nonAdminToken) {
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(nonAdminToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(36)
    void addNotaryAuthorization_nsAdminToken_success() {
        var response = verifySuccessResponse(addNotaryAuthorizationSuccess(TEST_NS_1, TEST_NOTARY_ISS_1, 
                TEST_NOTARY_SUB_1, oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        assertNotNull(response.getResponseBody().getId());
        assertEquals(TEST_NOTARY_ISS_1, response.getResponseBody().getIss());
        notaryAuthId1 = response.getResponseBody().getId();

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(37)
    void addNotaryAuthorization_secondClient_success() {
        var response = verifySuccessResponse(addNotaryAuthorizationSuccess(TEST_NS_1, TEST_NOTARY_ISS_2,
                TEST_NOTARY_SUB_2, oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        notaryAuthId2 = response.getResponseBody().getId();

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(38)
    void addNotaryAuthorization_duplicateClient_conflict() {
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.CONFLICT), HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Get Notary Authorization Tests ====================

    @TestTemplate
    @Order(39)
    void getNotaryAuthorization_nsAdminToken_success() {
        var response = verifySuccessResponse(getNotaryAuthorizationSuccess(TEST_NS_1, notaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        assertEquals(notaryAuthId1, response.getResponseBody().getId());
        assertEquals(TEST_NOTARY_ISS_1, response.getResponseBody().getIss());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(40)
    void getNotaryAuthorization_operatorToken_forbidden() {
        // Operator cannot get notary authorizations
        verifyErrorResponse(getNotaryAuthorizationError(TEST_NS_1, notaryAuthId1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(41)
    void getNotaryAuthorization_nonexistentClient_notFound() {
        verifyErrorResponse(getNotaryAuthorizationError(TEST_NS_1, "nonexistent-id",
                oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== List Notary Authorizations Tests ====================

    @TestTemplate
    @Order(42)
    void listNotaryAuthorizations_nsAdminToken_success() {
        var response = verifySuccessResponse(listNotaryAuthorizationsSuccess(TEST_NS_1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        var authList = response.getResponseBody().getAuthorizations();
        assertNotNull(authList);
        assertEquals(2, authList.size());

        var issuers = authList.stream().map(AuthorizationInfo::getIss).collect(Collectors.toSet());
        assertTrue(issuers.contains(TEST_NOTARY_ISS_1));
        assertTrue(issuers.contains(TEST_NOTARY_ISS_2));

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(43)
    void listNotaryAuthorizations_operatorToken_forbidden() {
        // Operator cannot list notary authorizations
        verifyErrorResponse(listNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Delete Non-Notary Authorization Tests ====================

    @TestTemplate
    @Order(44)
    void deleteNonNotaryAuthorization_operatorToken_forbidden() {
        // Operator cannot delete Non-Notary authorizations (only NS-Admin can)
        verifyErrorResponse(deleteNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(45)
    @MethodSource("unexpiredNonNsAdminTenantTokenTypesWithVariants")
    void deleteNonNotaryAuthorization_nonNsAdminTenantToken_forbidden(ITExecutionVariant variant, TestTokenType nonAdminToken) {
        verifyErrorResponse(deleteNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(nonAdminToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(46)
    void deleteNonNotaryAuthorization_nsAdminToken_success() {
        verifySuccessResponse(deleteNonNotaryAuthorizationSuccess(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(47)
    void deleteNonNotaryAuthorization_alreadyDeleted_noContent() {
        // Delete is idempotent
        verifySuccessResponse(deleteNonNotaryAuthorizationSuccess(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(48)
    void deleteNonNotaryAuthorization_nonexistentClient_noContent() {
        verifySuccessResponse(deleteNonNotaryAuthorizationSuccess(TEST_NS_1, "nonexistent-client-id",
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(49)
    void getNonNotaryAuthorization_afterDeletion_notFound() {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Delete Notary Authorization Tests ====================

    @TestTemplate
    @Order(50)
    void deleteNotaryAuthorization_operatorToken_forbidden() {
        verifyErrorResponse(deleteNotaryAuthorizationError(TEST_NS_1, notaryAuthId1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(51)
    void deleteNotaryAuthorization_nsAdminToken_success() {
        verifySuccessResponse(deleteNotaryAuthorizationSuccess(TEST_NS_1, notaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(52)
    void deleteNotaryAuthorization_alreadyDeleted_noContent() {
        verifySuccessResponse(deleteNotaryAuthorizationSuccess(TEST_NS_1, notaryAuthId1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(53)
    void listNotaryAuthorizations_afterDeletion_onlyOneRemaining() {
        var response = verifySuccessResponse(listNotaryAuthorizationsSuccess(TEST_NS_1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK);
        var authList = response.getResponseBody().getAuthorizations();
        assertEquals(1, authList.size());
        assertEquals(TEST_NOTARY_ISS_2, authList.get(0).getIss());
        assertEquals(notaryAuthId2, authList.get(0).getId());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Second Namespace Isolation Tests ====================

    @TestTemplate
    @Order(54)
    void createSecondNamespace_success() {
        var response = verifySuccessResponse(createNamespace(TEST_NS_2), HttpStatus.OK);
        assertEquals(effectiveNs(TEST_NS_2), response.getResponseBody().getNamespace());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(55)
    void addNonNotaryAuthorization_secondNamespace_operatorToken_success() {
        var response = verifySuccessResponse(addNonNotaryAuthorizationSuccess(TEST_NS_2, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK);
        assertNotNull(response.getResponseBody().getId());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(56)
    void getNonNotaryAuthorization_firstNamespaceAuth_fromSecondNamespace_notFound() {
        // Authorization from first namespace should not be visible in second namespace
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_2, nonNotaryAuthId2,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(57)
    void listNonNotaryAuthorizations_secondNamespace_onlySecondNamespaceAuths() {
        var response = verifySuccessResponse(listNonNotaryAuthorizationsSuccess(TEST_NS_2,
                oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK);
        var authList = response.getResponseBody().getAuthorizations();
        // Only one authorization (the one we just added)
        assertEquals(1, authList.size());
        assertEquals(TEST_NON_NOTARY_AUTH_ISS_1, authList.get(0).getIss());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Namespace Deletion Tests ====================

    @TestTemplate
    @Order(58)
    void deleteNamespace_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_1), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(59)
    void addNonNotaryAuthorization_namespaceDeleted_operatorToken_notFound() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(60)
    void getNonNotaryAuthorization_namespaceDeleted_operatorToken_notFound() {
        verifyErrorResponse(getNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId2,
                oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(61)
    void listNonNotaryAuthorizations_namespaceDeleted_operatorToken_notFound() {
        verifyErrorResponse(listNonNotaryAuthorizationsError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(62)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void addNonNotaryAuthorization_namespaceDeleted_nonOperatorToken_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_1, TEST_NON_NOTARY_AUTH_ISS_1, TEST_NON_NOTARY_AUTH_SUB_1,
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(63)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void deleteNonNotaryAuthorization_namespaceDeleted_nonOperatorToken_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(deleteNonNotaryAuthorizationError(TEST_NS_1, nonNotaryAuthId2,
                oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(64)
    void addNotaryAuthorization_namespaceDeleted_nsAdminToken_forbidden() {
        // NS-Admin's registration was in the deleted namespace
        verifyErrorResponse(addNotaryAuthorizationError(TEST_NS_1, TEST_NOTARY_ISS_1, TEST_NOTARY_SUB_1,
                oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Token with No Scopes Tests ====================

    @TestTemplate
    @Order(65)
    void addNonNotaryAuthorization_tenantTokenNoScopes_forbidden() {
        verifyErrorResponse(addNonNotaryAuthorizationError(TEST_NS_2, TEST_NON_NOTARY_AUTH_ISS_2, TEST_NON_NOTARY_AUTH_SUB_2,
                oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES), HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(66)
    void listNonNotaryAuthorizations_tenantTokenNoScopes_forbidden() {
        verifyErrorResponse(listNonNotaryAuthorizationsError(TEST_NS_2, oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // ==================== Cleanup Second Namespace ====================

    @TestTemplate
    @Order(67)
    void deleteSecondNamespace_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_2), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

}
