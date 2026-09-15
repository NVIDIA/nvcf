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

import com.google.common.collect.ContiguousSet;
import com.google.common.collect.Sets;
import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
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
class EntityIT extends IntegrationTestsBase {

    private static final String TEST_NS_1 = UUID.randomUUID().toString();
    private static final String TEST_NS_2 = UUID.randomUUID().toString();

    private static final String TEST_ET_1 = UUID.randomUUID().toString();
    private static final String TEST_ET_2 = UUID.randomUUID().toString();

    private static final String TEST_ENTITY = UUID.randomUUID().toString();

    private static final String TEST_SECRET_PATH_PREFIX = UUID.randomUUID().toString() + "/a/b";
  
    private static final Map<String, Object> sampleSecretData = Map.of("sampleList", List.of("a", "b"),
            "sampleMap", Map.of("c", "d"), "sampleString", "x", "sampleInt", 0, "sampleFloat", 0.5);

    private EntityExchangeResult<Void> checkEntityExistencePass(String namespace, String entityType, String entityId,
            String token) {
        return webTestClient.head()
                .uri(buildUrl("/v1/" + entityType + "/" + entityId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectBody()
                .isEmpty();
    }

    private EntityExchangeResult<ProblemDetail> checkEntityExistenceFail(String namespace, String entityType, String entityId,
            String token, HttpStatusCode errorCode) {
        return webTestClient.head()
                .uri(buildUrl("/v1/" + entityType + "/" + entityId))
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

    private EntityExchangeResult<Void> deleteEntitySuccess(String namespace, String entityType, String entityId, String token) {

        return webTestClient.delete()
                .uri(buildUrl("/v1/" + entityType + "/" + entityId))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.NO_CONTENT)
                .expectBody()
                .isEmpty();
    }

    private EntityExchangeResult<ProblemDetail> deleteEntityFail(String namespace, String entityType, String entityId,
            String token, HttpStatusCode errorCode) {

        return webTestClient.delete()
                .uri(buildUrl("/v1/" + entityType + "/" + entityId))
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

    private static Set<TestTokenType> unexpiredESSTokenTypes() {
        return Sets.union(unexpiredTenantTokenTypes(), Set.of(TestTokenType.OPERATOR));
    }

    private static Set<TestTokenType> expiredESSTokenTypes() {
        return Sets.union(expiredTenantTokenTypes(), Set.of(TestTokenType.OPERATOR_EXPIRED));
    }

    private static Set<TestTokenType> allESSTokenTypes() {
        return Sets.union(expiredESSTokenTypes(), unexpiredESSTokenTypes());
    }

    private static Set<TestTokenType> allTokenTypes() {
        return Sets.union(allESSTokenTypes(), Set.of(TestTokenType.NOTARY_SIGN_AUTH));
    }

    private static Set<TestTokenType> tenantWideAdminTokenTypes() {
        // Operator and NS Admin tokens cannot be used to call /v1/{entityType}/{entityId} APIs.
        return Set.of(TestTokenType.OPERATOR, TestTokenType.OPERATOR_EXPIRED,
                TestTokenType.NS_ADMIN, TestTokenType.NS_ADMIN_EXPIRED);
    }

    private static Set<TestTokenType> nonTenantWideAdminTokenTypes() {
        // OAuth2 tokens other than operator and NS admin tokens can be used to call the
        // `HEAD /v1/{entityType}/{entityId}` API as long as they are valid and the namespace
        // and entity-type exist.
        //
        // The `DELETE /v1/{entityType}/{entityId}` can only be called by tokens with the 
        // entities-admin scope.
        //
        return Sets.difference(allESSTokenTypes(), tenantWideAdminTokenTypes());
    }

    private static Set<TestTokenType> unexpiredNonTenantWideAdminTokenTypes() {
        return Sets.intersection(unexpiredESSTokenTypes(), nonTenantWideAdminTokenTypes());
    }

    private static Set<TestTokenType> expiredNonTenantWideAdminTokenTypes() {
        return Sets.intersection(expiredESSTokenTypes(), nonTenantWideAdminTokenTypes());
    }

    private static Set<TestTokenType> allNonEntityAdminESSTokenTypes() {
        return Sets.difference(allESSTokenTypes(),
                Set.of(TestTokenType.ENTITY_ADMIN, TestTokenType.ENTITY_ADMIN_EXPIRED));
    }

    private static Set<TestTokenType> unexpiredNonEntityAdminESSTokenTypes() {
        return Sets.intersection(allNonEntityAdminESSTokenTypes(), unexpiredESSTokenTypes());
    }

    // Variant-crossed sources (AuthDbStateVariants): each token is paired with every variant, with the
    // variant emitted as the leading argument (consumed by ITExecutionVariantExtension).
    private static Stream<Arguments> allTokenTypesWithVariants() {
        return withAuthDbStateVariants(allTokenTypes());
    }

    private static Stream<Arguments> expiredNonTenantWideAdminTokenTypesWithVariants() {
        return withAuthDbStateVariants(expiredNonTenantWideAdminTokenTypes());
    }

    private static Stream<Arguments> unexpiredNonTenantWideAdminTokenTypesWithVariants() {
        return withAuthDbStateVariants(unexpiredNonTenantWideAdminTokenTypes());
    }

    private static Stream<Arguments> unexpiredNonEntityAdminESSTokenTypesWithVariants() {
        return withAuthDbStateVariants(unexpiredNonEntityAdminESSTokenTypes());
    }

    private static Stream<Arguments> expiredTenantTokenTypesWithVariants() {
        return withAuthDbStateVariants(expiredTenantTokenTypes());
    }

    @ParameterizedTest
    @Order(1)
    @MethodSource("allTokenTypesWithVariants")
    void checkEntityExistence_anyToken_namespaceDoesNotExist_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(2)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntity_anyToken_namespaceDoesNotExist_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(3)
    void createNamespace_namespaceDoesNotExist_success() {

        verifySuccessResponse(createNamespace(TEST_NS_1), HttpStatus.OK);
        verifySuccessResponse(createNamespace(TEST_NS_2), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(4)
    @MethodSource("allTokenTypesWithVariants")
    void checkEntityExistence_anyToken_namespaceExists_authorizationNotRegistered_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(5)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntity_anyToken_namespaceExists_authorizationNotRegistered_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(6)
    void addAuthorizationsToNamespace_namespaceExists_success() {

        registerAllESSAuthorizations(TEST_NS_1);
        registerAllESSAuthorizations(TEST_NS_2);

        // 2 register NS-admin calls (actor: ESS-operator) =
        // 2 operator token-signature verifications.
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);

        // 2 register entity-admin authorization calls (actor: NS-admin) +
        // 2 register secret-admin authorization calls (actor: NS-admin) +
        // 2 register secret-consumer authorization calls (actor: NS-admin) +
        // 2 register notary-client authorization calls (actor: NS-admin) =
        // 8 tenant token-signature verifications.
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 8);
    }

    @TestTemplate
    @Order(7)
    void checkEntityExistence_operatorToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(8)
    void checkEntityExistence_expiredNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_401() {

        // Nit: Response codes are not symmetrical between attempts to poll the `HEAD /v1/{entityType}/{entityId}` API
        // between ESS operator and NS-admin tokens. This might need to be changed.
        //
        // Operator: 403 when either an expired or an unexpired token is used.
        // NS-Admin: 401 when an expired token is used, 403 when an unexpired token is used.

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN_EXPIRED), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(9)
    void checkEntityExistence_validNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(10)
    @MethodSource("expiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_expiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_401(ITExecutionVariant variant, TestTokenType expiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredNonTenantWideAdminToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(11)
    @MethodSource("unexpiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_unexpiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_404(ITExecutionVariant variant, TestTokenType unexpiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonTenantWideAdminToken), HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(12)
    void deleteEntity_operatorToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_403() {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(13)
    @MethodSource("unexpiredNonEntityAdminESSTokenTypesWithVariants")
    void deleteEntity_unexpiredNonEntityAdminESSToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_403(
            ITExecutionVariant variant, TestTokenType unexpiredNonEntityAdminESSToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonEntityAdminESSToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        var isOperator = unexpiredNonEntityAdminESSToken == TestTokenType.OPERATOR;

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(14)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void deleteEntity_expiredTenantToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_401(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredTenantToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(15)
    void deleteEntity_validEntityAdminToken_namespaceExists_authorizationsRegistered_entityTypeDoesNotExist_204() {

        verifySuccessResponse(deleteEntitySuccess(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                oauth2Tokens.get(TestTokenType.ENTITY_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(16)
    void createEntityTypesInNamespace_namespaceExists_success() {

        createEntityTypesInNamespace(TEST_NS_1, List.of(TEST_ET_1, TEST_ET_2), true);
        createEntityTypesInNamespace(TEST_NS_2, List.of(TEST_ET_1), false);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(17)
    void checkEntityExistence_operatorToken_namespaceExists_authorizationsRegistered_entityTypeExists_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(18)
    void checkEntityExistence_expiredNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_401() {

        // Nit: Response codes are not symmetrical between attempts to poll the `HEAD /v1/{entityType}/{entityId}` API
        // between ESS operator and NS-admin tokens. This might need to be changed.
        //
        // Operator: 403 when either an expired or an unexpired token is used.
        // NS-Admin: 401 when an expired token is used, 403 when an unexpired token is used.

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN_EXPIRED), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(19)
    void checkEntityExistence_validNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(20)
    @MethodSource("expiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_expiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_401(ITExecutionVariant variant, TestTokenType expiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredNonTenantWideAdminToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(21)
    @MethodSource("unexpiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_unexpiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_entityDoesNotExist_404(ITExecutionVariant variant, TestTokenType unexpiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonTenantWideAdminToken), HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(22)
    void insertFiveSecretsPerEntity_twoNamespaces_threeEntityTypesInTotal_oneEntityPerEntityType_success() {

        ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX + "/secret-" + i)
                .forEach(secretPathWithEntity -> {

                    verifySuccessResponse(createOrUpdateSecret(TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                            TEST_ET_1 + "/" + secretPathWithEntity, sampleSecretData), HttpStatus.OK);
                    verifySuccessResponse(createOrUpdateSecret(TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                            TEST_ET_2 + "/" + secretPathWithEntity, sampleSecretData), HttpStatus.OK);

                    verifySuccessResponse(createOrUpdateSecret(TEST_NS_2, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                            TEST_ET_1 + "/" + secretPathWithEntity, sampleSecretData), HttpStatus.OK);
                });

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 15);
    }

    @ParameterizedTest
    @Order(23)
    @MethodSource("unexpiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_unexpiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_entityExists_success(ITExecutionVariant variant, TestTokenType unexpiredNonTenantWideAdminToken) {

        verifySuccessResponse(checkEntityExistencePass(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonTenantWideAdminToken)),
                HttpStatus.OK, false);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(24)
    void deleteEntity_operatorToken_namespaceExists_entityTypeExists_403() {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);
        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(25)
    @MethodSource("unexpiredNonEntityAdminESSTokenTypesWithVariants")
    void deleteEntity_unexpiredNonEntityAdminESSToken_namespaceExists_authorizationsRegistered_entityTypeExists_entityExists_403(
            ITExecutionVariant variant, TestTokenType unexpiredNonEntityAdminESSToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonEntityAdminESSToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        var isOperator = unexpiredNonEntityAdminESSToken == TestTokenType.OPERATOR;

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(26)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void deleteEntity_expiredTenantToken_namespaceExists_authorizationsRegistered_entityTypeExists_entityExists_401(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredTenantToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(27)
    void deleteEntity_validEntityAdminToken_namespaceExists_authorizationsRegistered_entityTypeExists_entityExists_204() {

        verifySuccessResponse(deleteEntitySuccess(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                oauth2Tokens.get(TestTokenType.ENTITY_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(28)
    @MethodSource("unexpiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_unexpiredNonTenantWideAdminToken_namespaceExists_entityTypeExists_entityDeleted_404(
            ITExecutionVariant variant, TestTokenType unexpiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                oauth2Tokens.get(unexpiredNonTenantWideAdminToken), HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(29)
    void entityWasDeleted_namespaceExists_entityTypeExists_getEachSecretInEntity_404() {

        var secretPathPrefixWithEntity = TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX;
        var secretPaths = ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ET_1 + "/" + secretPathPrefixWithEntity + "/secret-" + i)
                .toList();

        var tenantNotaryToken = fetchTenantNotaryToken(TEST_NS_1, secretPaths);

        for (var secretPath : secretPaths) {
            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_1)
                                    .secretPath(secretPath)
                                    .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                                    .notaryToken(false)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.NOT_FOUND),
                    HttpStatus.NOT_FOUND);

            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_1)
                                    .secretPath(secretPath)
                                    .token(tenantNotaryToken)
                                    .notaryToken(true)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.NOT_FOUND),
                    HttpStatus.NOT_FOUND);
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 5);

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    @TestTemplate
    @Order(30)
    void entityWasDeleted_anotherEntityWithSameNameButDifferentEntityTypeHasSecrets_secretsShouldStillExist() {

        var secretPathPrefixWithEntity = TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX;
        var secretPaths = ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ET_2 + "/" + secretPathPrefixWithEntity + "/secret-" + i)
                .toList();

        var tenantNotaryToken = fetchTenantNotaryToken(TEST_NS_1, secretPaths);

        for (var secretPath : secretPaths) {
            var secretFetchedByOauth2Token = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                            .namespace(TEST_NS_1)
                            .secretPath(secretPath)
                            .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                            .notaryToken(false)
                            .version(null)
                            .queryType(null)
                            .build()),
                    HttpStatus.OK);
            Assertions.assertEquals(sampleSecretData,
                    secretFetchedByOauth2Token.getResponseBody().getData().getData());

            var secretFetchedByNotaryToken = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                            .namespace(TEST_NS_1)
                            .secretPath(secretPath)
                            .token(tenantNotaryToken)
                            .notaryToken(true)
                            .version(null)
                            .queryType(null)
                            .build()),
                    HttpStatus.OK);
            Assertions.assertEquals(sampleSecretData,
                    secretFetchedByNotaryToken.getResponseBody().getData().getData());
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 5);

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    @TestTemplate
    @Order(31)
    void entityWasDeleted_anotherEntityWithSameNameButDifferentNamespaceHasSecrets_secretsShouldStillExist() {

        var secretPathPrefixWithEntity = TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX;
        var secretPaths = ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ET_1 + "/" + secretPathPrefixWithEntity + "/secret-" + i)
                .toList();

        var tenantNotaryToken = fetchTenantNotaryToken(TEST_NS_2, secretPaths);

        for (var secretPath : secretPaths) {
            var secretFetchedByOauth2Token = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                            .namespace(TEST_NS_2)
                            .secretPath(secretPath)
                            .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                            .notaryToken(false)
                            .version(null)
                            .queryType(null)
                            .build()),
                    HttpStatus.OK);
            Assertions.assertEquals(sampleSecretData,
                    secretFetchedByOauth2Token.getResponseBody().getData().getData());

            var secretFetchedByNotaryToken = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                            .namespace(TEST_NS_2)
                            .secretPath(secretPath)
                            .token(tenantNotaryToken)
                            .notaryToken(true)
                            .version(null)
                            .queryType(null)
                            .build()),
                    HttpStatus.OK);
            Assertions.assertEquals(sampleSecretData,
                    secretFetchedByNotaryToken.getResponseBody().getData().getData());
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 5);

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    @TestTemplate
    @Order(32)
    void deleteEntityType_namespaceAndEntityTypeExist_success() {
        verifySuccessResponse(deleteEntityTypeSuccess(TEST_NS_1, TEST_ET_2, oauth2Tokens.get(TestTokenType.NS_ADMIN)),
                HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(33)
    void entityTypeWasDeleted_namespaceStillExists_entityTypeHadAnEntityBefore_secretsInEntityMustNotExist() {

        var secretPathPrefixWithEntity = TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX;
        var secretPaths = ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ET_2 + "/" + secretPathPrefixWithEntity + "/secret-" + i)
                .toList();

        var tenantNotaryToken = fetchTenantNotaryToken(TEST_NS_1, secretPaths);

        for (var secretPath : secretPaths) {
            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_1)
                                    .secretPath(secretPath)
                                    .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                                    .notaryToken(false)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.NOT_FOUND),
                    HttpStatus.NOT_FOUND);

            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_1)
                                    .secretPath(secretPath)
                                    .token(tenantNotaryToken)
                                    .notaryToken(true)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.NOT_FOUND),
                    HttpStatus.NOT_FOUND);
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 5);

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    @TestTemplate
    @Order(34)
    void checkEntityExistence_operatorToken_namespaceExists_authorizationsRegistered_entityTypeWasDeleted_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(35)
    void checkEntityExistence_expiredNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeWasDeleted_401() {

        // Nit: Response codes are not symmetrical between attempts to poll the `HEAD /v1/{entityType}/{entityId}` API
        // between ESS operator and NS-admin tokens. This might need to be changed.
        //
        // Operator: 403 when either an expired or an unexpired token is used.
        // NS-Admin: 401 when an expired token is used, 403 when an unexpired token is used.

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN_EXPIRED), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(36)
    void checkEntityExistence_validNSAdminToken_namespaceExists_authorizationsRegistered_entityTypeWasDeleted_403() {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.NS_ADMIN), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(37)
    @MethodSource("expiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_expiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeWasDeleted_401(ITExecutionVariant variant, TestTokenType expiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredNonTenantWideAdminToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @ParameterizedTest
    @Order(38)
    @MethodSource("unexpiredNonTenantWideAdminTokenTypesWithVariants")
    void checkEntityExistence_unexpiredNonTenantWideAdminToken_namespaceExists_authorizationsRegistered_entityTypeWasDeleted_404(ITExecutionVariant variant, TestTokenType unexpiredNonTenantWideAdminToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonTenantWideAdminToken), HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(39)
    void deleteEntity_operatorToken_namespaceExists_authorizationsRegistered_entityTypeWasAlreadyDeleted_403() {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(TestTokenType.OPERATOR_EXPIRED), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(40)
    @MethodSource("unexpiredNonEntityAdminESSTokenTypesWithVariants")
    void deleteEntity_unexpiredNonEntityAdminESSToken_namespaceExists_authorizationsRegistered_entityTypeWasAlreadyDeleted_403(
            ITExecutionVariant variant, TestTokenType unexpiredNonEntityAdminESSToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(unexpiredNonEntityAdminESSToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        var isOperator = unexpiredNonEntityAdminESSToken == TestTokenType.OPERATOR;

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", isOperator ? 0 : 1);
    }

    @ParameterizedTest
    @Order(41)
    @MethodSource("expiredTenantTokenTypesWithVariants")
    void deleteEntity_expiredTenantToken_namespaceExists_authorizationsRegistered_entityTypeWasAlreadyDeleted_401(
            ITExecutionVariant variant, TestTokenType expiredTenantToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(expiredTenantToken), HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(42)
    void deleteEntity_validEntityAdminToken_namespaceExists_authorizationsRegistered_entityTypeWasAlreadyDeleted_204() {

        verifySuccessResponse(deleteEntitySuccess(TEST_NS_1, TEST_ET_1, TEST_ENTITY,
                oauth2Tokens.get(TestTokenType.ENTITY_ADMIN)), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(43)
    void deleteNamespace_namespaceExists_success() {
        verifySuccessResponse(deleteNamespace(TEST_NS_2), HttpStatus.NO_CONTENT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(44)
    void namespaceWasDeleted_namespaceHadAnEntityBefore_secretsInEntityMustBeInaccessible_403() {

        var secretPathPrefixWithEntity = TEST_ENTITY + "/" + TEST_SECRET_PATH_PREFIX;
        var secretPaths = ContiguousSet.closedOpen(0, 5)
                .stream()
                .map(i -> TEST_ET_1 + "/" + secretPathPrefixWithEntity + "/secret-" + i)
                .toList();

        var tenantNotaryToken = fetchTenantNotaryToken(TEST_NS_2, secretPaths);

        for (var secretPath : secretPaths) {
            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_2)
                                    .secretPath(secretPath)
                                    .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                                    .notaryToken(false)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.FORBIDDEN),
                    HttpStatus.FORBIDDEN);

            verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                    .namespace(TEST_NS_2)
                                    .secretPath(secretPath)
                                    .token(tenantNotaryToken)
                                    .notaryToken(true)
                                    .version(null)
                                    .queryType(null)
                                    .build(),
                            HttpStatus.FORBIDDEN),
                    HttpStatus.FORBIDDEN);
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    @ParameterizedTest
    @Order(45)
    @MethodSource("allTokenTypesWithVariants")
    void checkEntityExistence_anyToken_namespaceWasDeleted_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(checkEntityExistenceFail(TEST_NS_2, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }

    @ParameterizedTest
    @Order(46)
    @MethodSource("allTokenTypesWithVariants")
    void deleteEntity_anyToken_namespaceWasAlreadyDeleted_403(ITExecutionVariant variant, TestTokenType anyToken) {

        verifyErrorResponse(deleteEntityFail(TEST_NS_2, TEST_ET_1, TEST_ENTITY,
                        oauth2Tokens.get(anyToken), HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
    }
}
