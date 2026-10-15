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

import com.google.common.collect.Sets;
import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.controller.response.ListEntityTypesResponse;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.List;
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
class EntityTypeIT extends IntegrationTestsBase {

    private static final String TEST_NS_1 = UUID.randomUUID().toString();
    private static final String TEST_NS_2 = UUID.randomUUID().toString();

    private static final String TEST_ET_1 = UUID.randomUUID().toString();
    private static final String TEST_ET_2 = UUID.randomUUID().toString();

    private EntityExchangeResult<EntityTypeInfo> createEntityTypeSuccess(String namespace, String entityType,
            String token) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildEntityTypeRequest(entityType))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(EntityTypeInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> createEntityTypeError(String namespace, String entityType,
            String token, HttpStatusCode errorCode) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .bodyValue(buildEntityTypeRequest(entityType))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> deleteEntityTypeError(String namespace, String entityType,
            String token, HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
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

    private EntityExchangeResult<EntityTypeInfo> getEntityTypeSuccess(String namespace, String entityType, String token) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(EntityTypeInfo.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> getEntityTypeError(String namespace, String entityType,
            String token, HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
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

    private EntityExchangeResult<ListEntityTypesResponse> listEntityTypesSuccess(String namespace, String token) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(ListEntityTypesResponse.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> listEntityTypesError(String namespace, String token,
            HttpStatusCode errorCode) {
        return webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types"))
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

    private static Set<TestTokenType> unexpiredTenantTokenTypes() {
        return Set.of(TestTokenType.NS_ADMIN, TestTokenType.ENTITY_ADMIN, TestTokenType.SECRET_ADMIN,
                TestTokenType.SECRET_CONSUMER);
    }

    private static Set<TestTokenType> expiredTenantTokenTypes() {
        return Set.of(TestTokenType.NS_ADMIN_EXPIRED, TestTokenType.ENTITY_ADMIN_EXPIRED,
                TestTokenType.SECRET_ADMIN_EXPIRED, TestTokenType.SECRET_CONSUMER_EXPIRED);
    }

    private static Set<TestTokenType> allTenantTokenTypes() {
         return Sets.union(unexpiredTenantTokenTypes(), expiredTenantTokenTypes());
    }

    private static Set<TestTokenType> allNonOperatorTokenTypes() {
        return Sets.union(allTenantTokenTypes(), Set.of(TestTokenType.NOTARY_SIGN_AUTH));
    }

    private static Set<TestTokenType> allTokenTypes() {
        return Sets.union(allNonOperatorTokenTypes(),
                Set.of(TestTokenType.OPERATOR, TestTokenType.OPERATOR_EXPIRED));
    }

    private static Set<TestTokenType> unexpiredNonNamespaceAdminTenantTokens() {
        return Sets.difference(unexpiredTenantTokenTypes(), Set.of(TestTokenType.NS_ADMIN));
    }

    private static Set<TestTokenType> unexpiredNonNamespaceAdminESSTokens() {
        return Sets.difference(
                Sets.union(unexpiredTenantTokenTypes(), Set.of(TestTokenType.OPERATOR)),
                Set.of(TestTokenType.NS_ADMIN)
        );
    }

    @ParameterizedTest
    @Order(1)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void createEntityType_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(2)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void getEntityType_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(3)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void listEntityTypes_notOperatorToken_namespaceDoesNotExist_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(4)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntityType_namespaceDoesNotExist_anyTokenIncludingOperatorTokens_forbidden(ITExecutionVariant variant, TestTokenType anyToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(anyToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(5)
    void createEntityType_expiredOperatorToken_namespaceDoesNotExist_unauthorized() {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(6)
    void getEntityType_expiredOperatorToken_namespaceDoesNotExist_unauthorized() {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(7)
    void listEntityTypes_expiredOperatorToken_namespaceDoesNotExist_unauthorized() {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(8)
    void createEntityType_validOperatorToken_namespaceDoesNotExist_404() {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(9)
    void getEntityType_validOperatorToken_namespaceDoesNotExist_404() {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(10)
    void listEntityTypes_validOperatorToken_namespaceDoesNotExist_404() {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(11)
    void createNamespace_namespaceDoesNotAlreadyExist_success() {
        var response = verifySuccessResponse(createNamespace(TEST_NS_1), HttpStatus.OK);
        var namespaceInfo = response.getResponseBody();
        assertEquals(effectiveNs(TEST_NS_1), namespaceInfo.getNamespace());
        assertNotNull(namespaceInfo.getCreatedAt());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(12)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntityType_namespaceExists_noAuthorizationsRegistered_anyTokenIncludingOperatorTokens_forbidden(
            ITExecutionVariant variant, TestTokenType anyToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(anyToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(13)
    void addTenantAuthorizationToNamespace_success() {

        verifySuccessResponse(addAuthorization(TEST_NS_1, integrationTestProperties.getTenant().getNsAdmin().getIss(),
                integrationTestProperties.getTenant().getNsAdmin().getSub(), true,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_1, integrationTestProperties.getTenant().getEntityAdmin().getIss(),
                integrationTestProperties.getTenant().getEntityAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_1, integrationTestProperties.getTenant().getSecretAdmin().getIss(),
                integrationTestProperties.getTenant().getSecretAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_1, integrationTestProperties.getTenant().getSecretConsumer().getIss(),
                integrationTestProperties.getTenant().getSecretConsumer().getSub(), false,
                false), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
    }

    @TestTemplate
    @Order(14)
    void createEntityType_namespaceExists_tokenSubIsNotRegistered_forbidden() {
        // OAuth2 token not targeted at an ESS endpoint, whose sub isn't registered with an ESS namespace.
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.NOTARY_SIGN_AUTH),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(15)
    void getEntityType_namespaceExists_tokenSubIsNotRegistered_forbidden() {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.NOTARY_SIGN_AUTH),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(16)
    void listEntityTypes_namespaceExists_tokenSubIsNotRegistered_forbidden() {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(TestTokenType.NOTARY_SIGN_AUTH),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(17)
    void deleteEntityType_namespaceExists_tokenSubIsNotRegistered_forbidden() {
        // OAuth2 token not targeted at an ESS endpoint, whose sub isn't registered with an ESS namespace.
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.NOTARY_SIGN_AUTH),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(18)
    @MethodSource("unexpiredNonNamespaceAdminESSTokensWithVariants")
    void deleteEntityType_validESSToken_namespaceExists_entityTypeDoesNotExist_notNamespaceAdmin_forbidden(
            ITExecutionVariant variant, TestTokenType validNonNamespaceAdminESSToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(validNonNamespaceAdminESSToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        var isOperator = validNonNamespaceAdminESSToken == TestTokenType.OPERATOR;

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", isOperator ? 0 : 1);
    }

    @TestTemplate
    @Order(19)
    void deleteEntityType_validNamespaceAdminToken_namespaceExists_entityTypeDoesNotExist_204() {

        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_1, TEST_ET_1,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(20)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void deleteEntityType_expiredTenantToken_namespaceExists_tokenSubIsRegistered_unauthorized(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(expiredTenantToken),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(21)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void createEntityType_expiredTenantToken_namespaceExists_tokenSubIsRegistered_unauthorized(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(expiredTenantToken),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(22)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void getEntityType_expiredTenantToken_namespaceExists_tokenSubIsRegistered_unauthorized(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(expiredTenantToken),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(23)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void listEntityTypes_namespaceExists_tokenSubIsNotRegistered_unauthorized(ITExecutionVariant variant, TestTokenType expiredTenantToken) {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(expiredTenantToken),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(24)
    @MethodSource("unexpiredNonNamespaceAdminTenantTokensWithVariants")
    void createEntityType_validTenantToken_namespaceExists_tenantIsNotNamespaceAdmin_forbidden(
            ITExecutionVariant variant, TestTokenType nonAdminTenantToken) {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonAdminTenantToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(25)
    @MethodSource("unexpiredNonNamespaceAdminTenantTokensWithVariants")
    void getEntityType_validTenantToken_namespaceExists_tenantIsNotNamespaceAdmin_forbidden(
            ITExecutionVariant variant, TestTokenType nonAdminTenantToken) {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonAdminTenantToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    // CRL = Create, Retrieve, List. Deletion can only be done by an NS-admin and not by an operator.
    private static Stream<Arguments> validEntityTypeCRLArgs() {
        return Stream.of(
            Arguments.of(TestTokenType.OPERATOR, TEST_NS_1, TEST_ET_1),
            Arguments.of(TestTokenType.NS_ADMIN, TEST_NS_1, TEST_ET_2)
        );
    }

    private static Stream<Arguments> validEntityTypeCRLArgsSecondNamespace() {
        return Stream.of(
            Arguments.of(TestTokenType.OPERATOR, TEST_NS_2, TEST_ET_1),
            Arguments.of(TestTokenType.NS_ADMIN, TEST_NS_2, TEST_ET_2)
        );
    }

    // Variant-crossed sources (AuthDbStateVariants): each base argument tuple is paired with every
    // variant, with the variant emitted as the leading argument (consumed by ITExecutionVariantExtension).
    private static Stream<Arguments> allNonOperatorTokenTypesWithVariants() {
        return withAuthDbStateVariants(allNonOperatorTokenTypes());
    }

    private static Stream<Arguments> allTokenTypesWithVariants() {
        return withAuthDbStateVariants(allTokenTypes());
    }

    private static Stream<Arguments> expiredTenantTokenTypesWithVariants() {
        return withAuthDbStateVariants(expiredTenantTokenTypes());
    }

    private static Stream<Arguments> unexpiredNonNamespaceAdminESSTokensWithVariants() {
        return withAuthDbStateVariants(unexpiredNonNamespaceAdminESSTokens());
    }

    private static Stream<Arguments> unexpiredNonNamespaceAdminTenantTokensWithVariants() {
        return withAuthDbStateVariants(unexpiredNonNamespaceAdminTenantTokens());
    }

    private static Stream<Arguments> validEntityTypeCRLArgsWithVariants() {
        return withAuthDbStateVariants(validEntityTypeCRLArgs());
    }

    private static Stream<Arguments> validEntityTypeCRLArgsSecondNamespaceWithVariants() {
        return withAuthDbStateVariants(validEntityTypeCRLArgsSecondNamespace());
    }

    @ParameterizedTest
    @Order(26)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void getEntityType_validAuthorizedToken_namespaceExists_entityTypeDoesNotExist_404(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        verifyErrorResponse(getEntityTypeError(namespace, entityType,
                        oauth2Tokens.get(validAuthorizedToken), HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(27)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void listEntityTypes_namespaceExists_noEntityTypesInNamespace_emptyList(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityTypeIgnored) {

        var response = verifySuccessResponse(listEntityTypesSuccess(TEST_NS_1,
                oauth2Tokens.get(validAuthorizedToken)), HttpStatus.OK);

        var listedEntityTypes = response.getResponseBody()
                .getEntityTypes()
                .stream()
                .map(et -> et.getName())
                .toList();

        assertEquals(List.of(), listedEntityTypes);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(28)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void createEntityType_validAuthorizedToken_namespaceExists_entityTypeDoesNotExist_success(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        var response = verifySuccessResponse(createEntityTypeSuccess(namespace, entityType,
                        oauth2Tokens.get(validAuthorizedToken)),
                HttpStatus.OK);

        assertEquals(entityType, response.getResponseBody().getName());

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(29)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void createEntityType_validAuthorizedToken_namespaceExists_entityTypeExists_409(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        verifyErrorResponse(createEntityTypeError(namespace, entityType, oauth2Tokens.get(validAuthorizedToken),
                        HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(30)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void getEntityType_validAuthorizedToken_namespaceExists_entityTypeExists_success(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        var response = verifySuccessResponse(getEntityTypeSuccess(namespace, entityType,
                        oauth2Tokens.get(validAuthorizedToken)),
                HttpStatus.OK);

        assertEquals(entityType, response.getResponseBody().getName());

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(31)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void listEntityTypes_validAuthorizedToken_namespaceExists_twoEntityTypesInNamespace_listBoth(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityTypeIgnored) {

        var response = verifySuccessResponse(listEntityTypesSuccess(namespace,
                oauth2Tokens.get(validAuthorizedToken)), HttpStatus.OK);

        var listedEntityTypes = response.getResponseBody()
                .getEntityTypes()
                .stream()
                .map(et -> et.getName())
                .collect(Collectors.toSet());

        assertEquals(Set.of(TEST_ET_1, TEST_ET_2), listedEntityTypes);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(32)
    @MethodSource("unexpiredNonNamespaceAdminESSTokensWithVariants")
    void deleteEntityType_validESSToken_namespaceExists_entityTypeExists_notNamespaceAdmin_forbidden(
            ITExecutionVariant variant, TestTokenType validNonNamespaceAdminESSToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(validNonNamespaceAdminESSToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        var isOperator = validNonNamespaceAdminESSToken == TestTokenType.OPERATOR;

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", isOperator ? 0 : 1);
    }

    @TestTemplate
    @Order(33)
    void createSecondNamespace_namespaceDoesNotAlreadyExist_success() {
        var response = verifySuccessResponse(createNamespace(TEST_NS_2), HttpStatus.OK);
        var namespaceInfo = response.getResponseBody();
        assertEquals(effectiveNs(TEST_NS_2), namespaceInfo.getNamespace());
        assertNotNull(namespaceInfo.getCreatedAt());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(34)
    void addTenantAuthorizationToSecondNamespace_success() {

        verifySuccessResponse(addAuthorization(TEST_NS_2, integrationTestProperties.getTenant().getNsAdmin().getIss(),
                integrationTestProperties.getTenant().getNsAdmin().getSub(), true,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_2, integrationTestProperties.getTenant().getEntityAdmin().getIss(),
                integrationTestProperties.getTenant().getEntityAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_2, integrationTestProperties.getTenant().getSecretAdmin().getIss(),
                integrationTestProperties.getTenant().getSecretAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(TEST_NS_2, integrationTestProperties.getTenant().getSecretConsumer().getIss(),
                integrationTestProperties.getTenant().getSecretConsumer().getSub(), false,
                false), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
    }

    @ParameterizedTest
    @Order(35)
    @MethodSource("validEntityTypeCRLArgsSecondNamespaceWithVariants")
    void getEntityType_validAuthorizedToken_bothNamespacesExist_entityTypeExistsInFirstNamespace_useSecondNamespace_404(
            ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        verifyErrorResponse(getEntityTypeError(namespace, entityType, oauth2Tokens.get(validAuthorizedToken),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(36)
    @MethodSource("validEntityTypeCRLArgsSecondNamespaceWithVariants")
    void listEntityTypes_validAuthorizedToken_bothNamespacesExist_twoEntityTypesInFirstNamespace_useSecondNamespace_emptyList(ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityTypeIgnored) {

        var response = verifySuccessResponse(listEntityTypesSuccess(namespace,
                oauth2Tokens.get(validAuthorizedToken)), HttpStatus.OK);

        var listedEntityTypes = response.getResponseBody()
                .getEntityTypes()
                .stream()
                .map(et -> et.getName())
                .toList();

        assertEquals(List.of(), listedEntityTypes);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @TestTemplate
    @Order(37)
    void deleteEntityType_validAuthorizedToken_bothNamespacesExist_entityTypeExistsInFirstNamespace_useSecondNamespace_204() {
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_2, TEST_ET_1,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_2, TEST_ET_2,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
    }

    @ParameterizedTest
    @Order(38)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void getEntityType_validAuthorizedToken_entityTypeExistsInFirstNamespace_afterDeletionAttemptFromSecondNamespace_stillExists(ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        var response = verifySuccessResponse(getEntityTypeSuccess(namespace, entityType,
                        oauth2Tokens.get(validAuthorizedToken)),
                HttpStatus.OK);

        assertEquals(entityType, response.getResponseBody().getName());

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(39)
    @MethodSource("validEntityTypeCRLArgsWithVariants")
    void listEntityTypes_validAuthorizedToken_twoEntityTypesInFirstNamespace_afterDeletionAttemptFromSecondNamespace_useFirstNamespace_listBoth(ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityTypeIgnored) {

        var response = verifySuccessResponse(listEntityTypesSuccess(namespace,
                oauth2Tokens.get(validAuthorizedToken)), HttpStatus.OK);

        var listedEntityTypes = response.getResponseBody()
                .getEntityTypes()
                .stream()
                .map(et -> et.getName())
                .collect(Collectors.toSet());

        assertEquals(Set.of(TEST_ET_1, TEST_ET_2), listedEntityTypes);

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(40)
    @MethodSource("validEntityTypeCRLArgsSecondNamespaceWithVariants")
    void createEntityType_validAuthorizedToken_useSecondNamespace_secondNamespaceExists_entityTypeDoesNotExistInSecondNamespace_success(ITExecutionVariant variant, TestTokenType validAuthorizedToken, String namespace, String entityType) {

        var response = verifySuccessResponse(createEntityTypeSuccess(namespace, entityType,
                        oauth2Tokens.get(validAuthorizedToken)),
                HttpStatus.OK);

        assertEquals(entityType, response.getResponseBody().getName());

        var isOperator = validAuthorizedToken == TestTokenType.OPERATOR;
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 1 : 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json",
                isOperator ? 0 : 1);
    }

    @TestTemplate
    @Order(41)
    void deleteEntityType_validAuthorizedToken_namespaceExists_entityTypeExists_204() {

        // Delete one entity-type each from either namespace.
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_2, TEST_ET_2, oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
    }

    @TestTemplate
    @Order(42)
    void deleteEntityType_validAuthorizedToken_namespaceExists_entityTypeAlreadyDeleted_204() {

        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_2, TEST_ET_2, oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
    }

    @TestTemplate
    @Order(43)
    void createEntityType_validAuthorizedToken_namespaceExists_entityTypeWasDeletedPreviously_409() {

        // Attempt to create an entity-type in each namespace with the same name as an already-deleted entity-type.
        // Use an operator token and an NS-admin token respectively for the two API calls.
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.CONFLICT), HttpStatus.CONFLICT);
        verifyErrorResponse(createEntityTypeError(TEST_NS_2, TEST_ET_2, oauth2Tokens.get(TestTokenType.NS_ADMIN),
                HttpStatus.CONFLICT), HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(44)
    void getEntityType_validAuthorizedToken_namespaceExists_entityTypeWasDeletedPreviously_404() {

        // Attempt to fetch an already-deleted entity-type in each namespace. Use an operator token and an NS-admin
        // token respectively for the two API calls.
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);
        verifyErrorResponse(getEntityTypeError(TEST_NS_2, TEST_ET_2, oauth2Tokens.get(TestTokenType.NS_ADMIN),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(45)
    void listEntityTypes_validAuthorizedToken_namespaceExists_oneEntityTypeDeleted_listTheRest() {

        // Attempt to list entity-types in each namespace. Use an operator token and an NS-admin token
        // respectively for the two API calls.
        var responses = List.of(
                verifySuccessResponse(listEntityTypesSuccess(TEST_NS_1,
                        oauth2Tokens.get(TestTokenType.OPERATOR)), HttpStatus.OK),
                verifySuccessResponse(listEntityTypesSuccess(TEST_NS_2,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN)), HttpStatus.OK)
        );

        var entityTypeLists = responses.stream().map(

            response -> response.getResponseBody()
                    .getEntityTypes()
                    .stream()
                    .map(et -> et.getName())
                    .toList()
        )
                .toList();

        assertEquals(List.of(TEST_ET_2), entityTypeLists.get(0));
        assertEquals(List.of(TEST_ET_1), entityTypeLists.get(1));

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(46)
    void deleteNamespace_namespaceExists_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_1), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(47)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void createEntityType_notOperatorToken_namespaceWasDeletedEarlier_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(48)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void getEntityType_notOperatorToken_namespaceWasDeletedEarlier_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(nonOperatorToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(49)
    @MethodSource("allNonOperatorTokenTypesWithVariants")
    void listEntityTypes_notOperatorToken_namespaceWasDeletedEarlier_forbidden(ITExecutionVariant variant, TestTokenType nonOperatorToken) {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(nonOperatorToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(50)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntityType_namespaceWasDeletedEarlier_anyTokenIncludingOperatorTokens_forbidden(ITExecutionVariant variant, TestTokenType anyToken) {
        verifyErrorResponse(deleteEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(anyToken),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(51)
    void createEntityType_expiredOperatorToken_namespaceWasDeletedEarlier_unauthorized() {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(52)
    void getEntityType_expiredOperatorToken_namespaceWasDeletedEarlier_unauthorized() {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(53)
    void listEntityTypes_expiredOperatorToken_namespaceWasDeletedEarlier_unauthorized() {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(54)
    void createEntityType_validOperatorToken_namespaceWasDeletedEarlier_404() {
        verifyErrorResponse(createEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(55)
    void getEntityType_validOperatorToken_namespaceWasDeletedEarlier_404() {
        verifyErrorResponse(getEntityTypeError(TEST_NS_1, TEST_ET_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(56)
    void listEntityTypes_validOperatorToken_namespaceWasDeletedEarlier_404() {
        verifyErrorResponse(listEntityTypesError(TEST_NS_1, oauth2Tokens.get(TestTokenType.OPERATOR),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }
}
