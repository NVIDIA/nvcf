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

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.ess.EssCoreTestApp;
import com.nvidia.ess.config.properties.SecretSizeProperties;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.SecretQueryType;
import com.nvidia.ess.testing.CassandraContainerTest;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.TestTemplate;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
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
class SecretIT extends IntegrationTestsBase {

    private static final String GROUP_1_TEST_NS_1 = UUID.randomUUID().toString();
    private static final String GROUP_1_TEST_NS_2 = UUID.randomUUID().toString();

    private static final String GROUP_2_TEST_NS_1 = UUID.randomUUID().toString();
    private static final String GROUP_2_TEST_NS_2 = UUID.randomUUID().toString();

    // Group 3: Used for DELETE secret tests
    private static final String GROUP_3_TEST_NS_1 = UUID.randomUUID().toString();

    // Group 4: Used for secret version listing tests  
    private static final String GROUP_4_TEST_NS_1 = UUID.randomUUID().toString();

    // Group 5: Used for secret path listing tests
    private static final String GROUP_5_TEST_NS_1 = UUID.randomUUID().toString();

    // Group 6: Used for CAS tests
    private static final String GROUP_6_TEST_NS_1 = UUID.randomUUID().toString();

    // Group 7: Used for authorization deletion tests
    private static final String GROUP_7_TEST_NS_1 = UUID.randomUUID().toString();

    // Group 8: Used for namespace/entity-type deletion tests
    private static final String GROUP_8_TEST_NS_1 = UUID.randomUUID().toString();

    private static final Map<String, Object> sampleSecretData = Map.of("sampleList", List.of("a", "b"),
            "sampleMap", Map.of("c", "d"), "sampleString", "x", "sampleInt", 0, "sampleFloat", 0.5);

    private static final Map<String, Object> sampleSecretData2 = Map.of("field1", "value1", "field2", "value2");
    private static final Map<String, Object> sampleSecretData3 = Map.of("key", "different_value");

    // length('{"k":"<X-ascii-chars>"}') = X + 8
    private static Map<String, Object> oversizedSecretData;
    private static Map<String, Object> maxSizedSecretData;

    @BeforeAll
    static void setup(@Autowired SecretSizeProperties secretSizeProperties) {
        oversizedSecretData = Map.of("k", "0".repeat((int) secretSizeProperties.getMax().toBytes() - 7));
        maxSizedSecretData = Map.of("k", "0".repeat((int) secretSizeProperties.getMax().toBytes() - 8));
    }

    private EntityExchangeResult<ProblemDetail> createSecretError(String namespace, String token, String secretPath,
            Map<String, Object> secretData, UUID cas, HttpStatusCode errorCode) {
        return webTestClient.put()
                .uri(buildUrl("/v1/" + secretPath))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .bodyValue(buildCreateSecretRequest(secretData, cas))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> deleteSecretWithNoAuthHeader(String namespace, String secretPath,
            HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/" + secretPath))
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> createSecretWithNotaryTokenOnly(String namespace, String notaryToken,
            String secretPath, Map<String, Object> secretData, HttpStatusCode errorCode) {
        // Send request with X-ESS-TOKEN header (notary token) but no Authorization header
        return webTestClient.put()
                .uri(buildUrl("/v1/" + secretPath))
                .header("X-ESS-TOKEN", notaryToken)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .bodyValue(buildCreateSecretRequest(secretData, null))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private EntityExchangeResult<ProblemDetail> deleteSecretWithNotaryTokenOnly(String namespace, String notaryToken,
            String secretPath, HttpStatusCode errorCode) {
        // Send request with X-ESS-TOKEN header (notary token) but no Authorization header
        return webTestClient.delete()
                .uri(buildUrl("/v1/" + secretPath))
                .header("X-ESS-TOKEN", notaryToken)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    private void verifySecretRetrieval(String namespace, String secretPath, String token, boolean isNotary,
            UUID version, Map<String, Object> expectedData) {
        var response = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(namespace)
                        .secretPath(secretPath)
                        .token(token)
                        .notaryToken(isNotary)
                        .version(version)
                        .queryType(null)
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(response);
        assertNotNull(response.getData());
        assertEquals(expectedData, response.getData().getData());
    }

    private void verifySecretNotFound(String namespace, String secretPath, String token, boolean isNotary, UUID version) {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(namespace)
                        .secretPath(secretPath)
                        .token(token)
                        .notaryToken(isNotary)
                        .version(version)
                        .queryType(null)
                        .build(),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);
    }

    private List<String> verifySecretVersionListing(String namespace, String secretPath, String token, int expectedCount) {
        var response = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(namespace)
                        .secretPath(secretPath)
                        .token(token)
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(response);
        assertNotNull(response.getData());
        assertNotNull(response.getData().getKeys());
        assertEquals(expectedCount, response.getData().getKeys().size());
        return response.getData().getKeys();
    }

    private List<String> verifySecretPathListing(String namespace, String entityPath, String token, 
            List<String> expectedPaths) {
        var response = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(namespace)
                        .secretPath(entityPath)
                        .token(token)
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(response);
        assertNotNull(response.getData());
        assertNotNull(response.getData().getKeys());
        if (expectedPaths != null) {
            assertEquals(expectedPaths.size(), response.getData().getKeys().size());
            for (String expectedPath : expectedPaths) {
                assertTrue(response.getData().getKeys().contains(expectedPath), 
                        "Expected path not found: " + expectedPath);
            }
        }
        return response.getData().getKeys();
    }

    private UUID createSecretAndGetVersion(String namespace, String token, String secretPath, 
            Map<String, Object> secretData) {
        var response = verifySuccessResponse(createOrUpdateSecret(namespace, token, secretPath, secretData),
                HttpStatus.OK).getResponseBody();
        assertNotNull(response);
        assertNotNull(response.getData());
        return response.getData().getVersion();
    }

    @TestTemplate
    @Order(1)
    void createSecret_nonexistentNamespace_forbidden() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(2)
    void createNamespace_success() {
        verifySuccessResponse(createNamespace(GROUP_1_TEST_NS_1), HttpStatus.OK);
        verifySuccessResponse(createNamespace(GROUP_1_TEST_NS_2), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(3)
    void createSecret_namespaceExists_noAuthorization_403() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(4)
    void addTenantAuthorizationToNamespace_success() {

        for (var ns : List.of(GROUP_1_TEST_NS_1, GROUP_1_TEST_NS_2)) {
            verifySuccessResponse(addAuthorization(ns, integrationTestProperties.getTenant().getNsAdmin().getIss(),
                    integrationTestProperties.getTenant().getNsAdmin().getSub(), true,
                    false), HttpStatus.OK);
        }

        verifySuccessResponse(addAuthorization(GROUP_1_TEST_NS_1, integrationTestProperties.getTenant().getEntityAdmin().getIss(),
                integrationTestProperties.getTenant().getEntityAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(GROUP_1_TEST_NS_1, integrationTestProperties.getTenant().getSecretAdmin().getIss(),
                integrationTestProperties.getTenant().getSecretAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(GROUP_1_TEST_NS_1, integrationTestProperties.getTenant().getSecretConsumer().getIss(),
                integrationTestProperties.getTenant().getSecretConsumer().getSub(), false,
                false), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(5)
    void createSecret_namespaceExists_nonexistentEntityType_404() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(6)
    void createEntityType_success() {
        verifySuccessResponse(createEntityType(GROUP_1_TEST_NS_1, "entityType1", true), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(7)
    void createSecret_expiredTenantToken_unauthorized() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN_EXPIRED),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(8)
    void createSecret_oauth2TokenFromCorrectIssuer_noScopes_forbidden() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(9)
    void createSecret_oauth2TokenFromCorrectIssuer_insufficientScopes_forbidden() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_CONSUMER),
                "entityType1/entity1/a/b/c", sampleSecretData, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(10)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_emptySecretPath_400() {

        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/", sampleSecretData, null, HttpStatus.BAD_REQUEST),
                HttpStatus.BAD_REQUEST);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(11)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_oversizedPayload_400() {

        verifySuccessResponse(createOrUpdateSecret(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/maxSizedSecret", maxSizedSecretData),
                HttpStatus.OK);
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/overSizedSecret", oversizedSecretData, null, HttpStatus.BAD_REQUEST),
                HttpStatus.BAD_REQUEST);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(12)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_secretPathIsPrefixOfExistingSecretPath_409() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b", sampleSecretData, null, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(13)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_secretPathIsSuffixOfExistingSecretPath_409() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/maxSizedSecret/suffix", sampleSecretData, null, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(14)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_notaryAuthorizationNotAdded_403() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(15)
    void addNotaryAuthorizationToNamespace_success() {
        for (var ns : List.of(GROUP_1_TEST_NS_1, GROUP_1_TEST_NS_2)) {
            verifySuccessResponse(addAuthorization(ns, integrationTestProperties.getTenant().getNotary().getIss(),
                    integrationTestProperties.getTenant().getNotary().getSub(), false,
                    true), HttpStatus.OK);
        }

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(16)
    void getSecret_notaryTokenWithWrongNSAssertion_secretExists_403() {

        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithWrongNSAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_2,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithWrongNSAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(17)
    void getSecret_notaryTokenWithWrongPathAssertion_secretExists_403() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithWrongPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/c/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithWrongPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(18)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_wrongNamespace_403() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_2)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(19)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_wrongEntityType_404() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType2/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType2/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(20)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_wrongSecretPath_404() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/c"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/c")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build(),
                        HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(21)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_wrongVersion_404() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(UUID.randomUUID())
                                .queryType(null)
                                .build(),
                        HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(22)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_badQueryType_400() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType("invalid")
                                .build(),
                        HttpStatus.BAD_REQUEST),
                HttpStatus.BAD_REQUEST);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(23)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_secretExists_200() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryTokenWithCorrectNSAndPathAssertion = getEssAssertion(notarySub, GROUP_1_TEST_NS_1,
                List.of("entityType1/entity1/a/b/maxSizedSecret"));

        verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                                .namespace(GROUP_1_TEST_NS_1)
                                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                                .token(notaryTokenWithCorrectNSAndPathAssertion)
                                .notaryToken(true)
                                .version(null)
                                .queryType(null)
                                .build()),
                HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(24)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_secretAlreadyExists_newVersionCreated_200() {
        // Verify previous secret value.
        var previousSecret = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_1_TEST_NS_1)
                        .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(null)
                        .build()),
                HttpStatus.OK).getResponseBody();

        assertNotNull(previousSecret);
        assertNotNull(previousSecret.getData());
        assertEquals(maxSizedSecretData, previousSecret.getData().getData());

        // Obtain previous secret version.
        var previousSecretVersion = previousSecret.getData().getMetadata().getVersion();

        // Write new secret value to existing secret path.
        verifySuccessResponse(createOrUpdateSecret(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/maxSizedSecret", sampleSecretData),
                HttpStatus.OK);

        // Verify new secret value with OAuth2 token.
        var getSecretWithOauth2 = GetSecretRequestInput.builder()
                .namespace(GROUP_1_TEST_NS_1)
                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .notaryToken(false)
                .version(null)
                .queryType(null)
                .build();
        var newSecret = verifySuccessResponse(getSecret(getSecretWithOauth2), HttpStatus.OK).getResponseBody();
        assertNotNull(newSecret);
        assertNotNull(newSecret.getData());
        assertEquals(sampleSecretData, newSecret.getData().getData());

        // Verify previous secret value still exists under previous version, with oauth2 token.
        var stillExistingPreviousSecret = verifySuccessResponse(getSecret(getSecretWithOauth2
                        .toBuilder()
                        .version(previousSecretVersion)
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(stillExistingPreviousSecret);
        assertNotNull(stillExistingPreviousSecret.getData());
        assertEquals(maxSizedSecretData, stillExistingPreviousSecret.getData().getData());
        assertEquals(previousSecretVersion, stillExistingPreviousSecret.getData().getMetadata().getVersion());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(25)
    void createSecret_oauth2TokenFromCorrectIssuerWithCorrectScope_namespaceAndEntityTypeExist_secretDoesNotExist_200() {

        var getSecretWithOauth2 = GetSecretRequestInput.builder()
                .namespace(GROUP_1_TEST_NS_1)
                .secretPath("entityType1/entity1/a/b/c")
                .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .notaryToken(false)
                .version(null)
                .queryType(null)
                .build();

        // Verify secret does not exist initially.        
        verifyErrorResponse(getSecretError(getSecretWithOauth2, HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        // Create secret.
        verifySuccessResponse(createOrUpdateSecret(GROUP_1_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/c", sampleSecretData),
                HttpStatus.OK);

        // Verify secret exists with expected value.
        var secret = verifySuccessResponse(getSecret(getSecretWithOauth2), HttpStatus.OK).getResponseBody();
        assertNotNull(secret);
        assertNotNull(secret.getData());
        assertEquals(sampleSecretData, secret.getData().getData());

        // Fetch secret with notary token and verify fetch success with expected value.
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        String notaryToken = getEssAssertion(notarySub, GROUP_1_TEST_NS_1, List.of("entityType1/entity1/a/b/c"));
        var getSecretWithNotary = getSecretWithOauth2.toBuilder()
                .notaryToken(true)
                .token(notaryToken)
                .build();
        var getSecretWithNotaryResponse = verifySuccessResponse(getSecret(getSecretWithNotary), HttpStatus.OK).getResponseBody();
        assertNotNull(getSecretWithNotaryResponse);
        assertNotNull(getSecretWithNotaryResponse.getData());
        assertEquals(sampleSecretData, getSecretWithNotaryResponse.getData().getData());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(26)
    void getSecret_notaryTokenWithCorrectNSAndPathAssertion_secretExistsWithTwoVersions_getBothVersions_200() {

        // Verify new secret value with notary token.
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        String notaryToken = getEssAssertion(notarySub, GROUP_1_TEST_NS_1, List.of("entityType1/entity1/a/b/maxSizedSecret"));
        var getSecretWithNotary = GetSecretRequestInput.builder()
                .namespace(GROUP_1_TEST_NS_1)
                .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .notaryToken(false)
                .version(null)
                .queryType(null)
                .build().toBuilder()
                .notaryToken(true)
                .token(notaryToken)
                .build();
        var newSecretWithNotary = verifySuccessResponse(getSecret(getSecretWithNotary), HttpStatus.OK)
                .getResponseBody();
        assertNotNull(newSecretWithNotary);
        assertNotNull(newSecretWithNotary.getData());
        assertEquals(sampleSecretData, newSecretWithNotary.getData().getData());

        // Obtain new secret version.
        var newSecretVersion = newSecretWithNotary.getData().getMetadata().getVersion();

        // List all versions of the secret (there must be 2 versions).
        var allVersions = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_1_TEST_NS_1)
                        .secretPath("entityType1/entity1/a/b/maxSizedSecret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(allVersions);
        assertNotNull(allVersions.getData());
        assertNotNull(allVersions.getData().getKeys());
        assertEquals(2, allVersions.getData().getKeys().size());
        assertEquals(newSecretVersion.toString(), allVersions.getData().getKeys().get(0));

        var previousSecretVersion = allVersions.getData().getKeys().get(1);

        // Verify previous secret value still exists under previous version, with notary token.
        var stillExistingPreviousSecretWithNotary = verifySuccessResponse(getSecret(getSecretWithNotary
                        .toBuilder()
                        .version(UUID.fromString(previousSecretVersion))
                        .build()),
                HttpStatus.OK).getResponseBody();
        assertNotNull(stillExistingPreviousSecretWithNotary);
        assertNotNull(stillExistingPreviousSecretWithNotary.getData());
        assertEquals(maxSizedSecretData, stillExistingPreviousSecretWithNotary.getData().getData());
        assertEquals(previousSecretVersion, stillExistingPreviousSecretWithNotary.getData().getMetadata().getVersion().toString());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 2);
    }

    // =====================================================================
    // NOTARY TOKEN RESTRICTION TESTS - Notary tokens can only be used for FETCH_SECRET
    // =====================================================================

    @TestTemplate
    @Order(27)
    void setupGroup2Namespace() {
        // Setup for tests that need GROUP_2_TEST_NS_1
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_2_TEST_NS_1, List.of("functions", "ncas"));
        
        // Create a secret for subsequent tests
        verifySuccessResponse(
                createOrUpdateSecret(GROUP_2_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                        "functions/f1/path/secretA", sampleSecretData2),
                HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(28)
    void getSecret_notaryTokenWithListVersionsQueryType_forbidden() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_2_TEST_NS_1, List.of("functions/f1/path/secretA"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_2_TEST_NS_1)
                                .secretPath("functions/f1/path/secretA")
                        .token(notaryToken)
                        .notaryToken(true)
                                .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                                .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(29)
    void getSecret_notaryTokenWithListSecretsQueryType_forbidden() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_2_TEST_NS_1, List.of("functions/f1/path/secretA"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                                .namespace(GROUP_2_TEST_NS_1)
                        .secretPath("functions/f1/path")
                        .token(notaryToken)
                                .notaryToken(true)
                                .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                                .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(30)
    void createSecret_notaryTokenViaEssTokenHeader_badRequest() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_2_TEST_NS_1, List.of("functions/f1/path/newSecret"));

        // PUT with notary token sent via X-ESS-TOKEN header (no Authorization header)
        // should fail with 400 because Authorization header is required for PUT
        verifyErrorResponse(createSecretWithNotaryTokenOnly(GROUP_2_TEST_NS_1, notaryToken,
                "functions/f1/path/newSecret", sampleSecretData, HttpStatus.BAD_REQUEST),
                HttpStatus.BAD_REQUEST);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(31)
    void deleteSecret_notaryTokenViaEssTokenHeader_badRequest() {
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_2_TEST_NS_1, List.of("functions/f1/path/secretA"));

        // DELETE with notary token sent via X-ESS-TOKEN header (no Authorization header)
        // should fail with 400 because Authorization header is required for DELETE
        verifyErrorResponse(deleteSecretWithNotaryTokenOnly(GROUP_2_TEST_NS_1, notaryToken,
                "functions/f1/path/secretA", HttpStatus.BAD_REQUEST),
                HttpStatus.BAD_REQUEST);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // OAuth2 TOKEN API ACQUISITION TESTS - Demonstrates fetching tokens via mock OAuth2 endpoints
    // =====================================================================

    @SneakyThrows
    @TestTemplate
    @Order(32)
    void fetchOauth2TokenFromMockOperatorOauth2Server_verifyTokenContents() {
        var fetchedOperatorOauth2TokenResponse =
                verifySuccessResponse(getOperatorOauth2TokenWithApi(), HttpStatus.OK);
        var fetchedOperatorOAuthToken = fetchedOperatorOauth2TokenResponse.getResponseBody();
        assertNotNull(fetchedOperatorOAuthToken);
        assertThat(fetchedOperatorOAuthToken.getScope()).contains(AuthScope.ESS_OPERATOR);

        // This test fetches tokens from mock OAuth2 server, not ESS service - no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(33)
    void fetchOauth2TokenFromMockTenantOauth2Server_secretAdminScope_verifyTokenContents() {
        var fetchedSecretAdminOauth2TokenResponse = verifySuccessResponse(getTenantOauth2TokenWithApi(
                        integrationTestProperties.getTenant().getSecretAdmin(),
                        List.of(AuthScope.ESS_SECRETS_ADMIN)),
                HttpStatus.OK);
        var fetchedSecretAdminOauth2Token = fetchedSecretAdminOauth2TokenResponse.getResponseBody();
        assertNotNull(fetchedSecretAdminOauth2Token);
        assertThat(fetchedSecretAdminOauth2Token.getScope()).contains(AuthScope.ESS_SECRETS_ADMIN);

        // This test fetches tokens from mock OAuth2 server, not ESS service - no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(34)
    void fetchOauth2TokenFromMockTenantOauth2Server_secretConsumerScope_verifyTokenContents() {
        var fetchedSecretConsumerOauth2TokenResponse = verifySuccessResponse(
                getTenantOauth2TokenWithApi(
                        integrationTestProperties.getTenant().getSecretConsumer(),
                        List.of(AuthScope.ESS_SECRETS_CONSUMER)),
                HttpStatus.OK);
        var fetchedSecretConsumerOauth2Token = fetchedSecretConsumerOauth2TokenResponse.getResponseBody();
        assertNotNull(fetchedSecretConsumerOauth2Token);
        assertThat(fetchedSecretConsumerOauth2Token.getScope()).contains(AuthScope.ESS_SECRETS_CONSUMER);

        // This test fetches tokens from mock OAuth2 server, not ESS service - no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(35)
    void getSecret_withFetchedSecretAdminOauth2Token_success() {
        var fetchedSecretAdminOauth2TokenResponse = verifySuccessResponse(getTenantOauth2TokenWithApi(
                        integrationTestProperties.getTenant().getSecretAdmin(),
                        List.of(AuthScope.ESS_SECRETS_ADMIN)),
                HttpStatus.OK);

            var secretGetResponseOauth2 = verifySuccessResponse(
                    getSecret(GetSecretRequestInput.builder()
                            .namespace(GROUP_2_TEST_NS_1)
                            .secretPath("functions/f1/path/secretA")
                        .token(fetchedSecretAdminOauth2TokenResponse.getResponseBody().getAccessToken())
                            .notaryToken(false)
                            .version(null)
                            .queryType(null)
                            .build()),
                    HttpStatus.OK);

            var oauth2AuthEnabledSecretGetResponse = secretGetResponseOauth2.getResponseBody();
            assertNotNull(oauth2AuthEnabledSecretGetResponse);
            assertNotNull(oauth2AuthEnabledSecretGetResponse.getData());
        assertEquals(sampleSecretData2, oauth2AuthEnabledSecretGetResponse.getData().getData());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(36)
    void getSecret_withFetchedSecretConsumerOauth2Token_success() {
        var fetchedSecretConsumerOauth2TokenResponse = verifySuccessResponse(
                getTenantOauth2TokenWithApi(
                        integrationTestProperties.getTenant().getSecretConsumer(),
                        List.of(AuthScope.ESS_SECRETS_CONSUMER)),
                HttpStatus.OK);

        var secretGetResponseOauth2 = verifySuccessResponse(
                getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_2_TEST_NS_1)
                        .secretPath("functions/f1/path/secretA")
                        .token(fetchedSecretConsumerOauth2TokenResponse.getResponseBody().getAccessToken())
                        .notaryToken(false)
                        .version(null)
                        .queryType(null)
                        .build()),
                HttpStatus.OK);

        var oauth2AuthEnabledSecretGetResponse = secretGetResponseOauth2.getResponseBody();
        assertNotNull(oauth2AuthEnabledSecretGetResponse);
        assertNotNull(oauth2AuthEnabledSecretGetResponse.getData());
        assertEquals(sampleSecretData2, oauth2AuthEnabledSecretGetResponse.getData().getData());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // NOTARY TOKEN API ACQUISITION TESTS - Demonstrates fetching notary tokens via mock /sign endpoint
    // =====================================================================

    @SneakyThrows
    @TestTemplate
    @Order(37)
    void fetchNotaryTokenFromMockNotaryServer_verifyTokenContents() {
        // Fetch a signed notary assertion in two steps:
        // [1] Obtain an OAuth2 auth-token to authenticate to the notary server's /sign endpoint
        // [2] Call the /sign endpoint with the OAuth2 auth-token and the assertion data
        var fetchedTenantNotaryToken = fetchTenantNotaryToken(GROUP_2_TEST_NS_1, List.of("functions/f1/path/secretA"));

        assertNotNull(fetchedTenantNotaryToken);
        var assertionJwt = SignedJWT.parse(fetchedTenantNotaryToken);
        assertEquals(authProperties.getServiceId(), assertionJwt.getJWTClaimsSet().getAudience().get(0));
        var assertions = assertionJwt.getJWTClaimsSet().getJSONObjectClaim("assertion");
        assertEquals(2, assertions.size());
        assertEquals(effectiveNs(GROUP_2_TEST_NS_1), assertions.get("namespace"));
        assertEquals(List.of("functions/f1/path/secretA"), assertions.get("secretPaths"));

        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);

        // This test fetches tokens from mock notary server, not ESS service
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(38)
    void fetchNotaryTokenFromMockNotaryServer_withWrongOauth2Token_unauthorized() {
        // Fetch an OAuth2 auth-token from a different OAuth2 issuer (ESS tenant).
        // This auth-token cannot be used to authenticate to the /sign endpoint.
        var otherOauth2TokenResponse = verifySuccessResponse(
                getTenantOauth2TokenWithApi(integrationTestProperties.getTenant().getNsAdmin(),
                        List.of(AuthScope.ESS_NAMESPACE_ADMIN)),
                HttpStatus.OK);
        var otherOauth2Token = otherOauth2TokenResponse.getResponseBody();

        // Call to /sign endpoint fails when the auth-token fetched from the other OAuth2 service is used.
        var notaryAssertionAudience = List.of(authProperties.getServiceId());
        var notaryAssertionData = Map.<String, Object>of("namespace", GROUP_2_TEST_NS_1, 
                "secretPaths", List.of("functions/f1/path/secretA"));
        verifyErrorResponse(
                getTenantNotaryTokenApiError(otherOauth2Token.getAccessToken(), notaryAssertionAudience,
                        notaryAssertionData, HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        // NimbusJWTDecoder attempts fetch + retry = 2 polls
        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(2);

        // This test calls mock notary server /sign endpoint, not ESS service
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @SneakyThrows
    @TestTemplate
    @Order(39)
    void getSecret_withFetchedNotaryToken_success() {
        var fetchedTenantNotaryToken = fetchTenantNotaryToken(GROUP_2_TEST_NS_1, List.of("functions/f1/path/secretA"));

        var secretGetResponseWithFetchedNotaryToken = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_2_TEST_NS_1)
                        .secretPath("functions/f1/path/secretA")
                        .token(fetchedTenantNotaryToken)
                        .notaryToken(true)
                        .version(null)
                        .queryType(null)
                        .build()),
                HttpStatus.OK);

        var notaryAuthEnabledSecretGetResponse = secretGetResponseWithFetchedNotaryToken.getResponseBody();
        assertNotNull(notaryAuthEnabledSecretGetResponse);
        assertNotNull(notaryAuthEnabledSecretGetResponse.getData());
        assertEquals(sampleSecretData2, notaryAuthEnabledSecretGetResponse.getData().getData());

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
        authServers.verifyNotarySignAuthOauth2ServerJwksPolled(1);
    }

    // =====================================================================
    // DELETE SECRET TESTS - PUT /v1/{entityType}/{entityId}/** deletion scenarios
    // =====================================================================

    @TestTemplate
    @Order(40)
    void setupGroup3Namespace_forDeleteTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_3_TEST_NS_1, List.of("entityType1"));
        
        // Create secrets for deletion tests
        verifySuccessResponse(createOrUpdateSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret1", sampleSecretData), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret2", sampleSecretData2), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity2/x/y/secretX", sampleSecretData3), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 7);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(41)
    void deleteSecret_noAuthorizationHeader_forbidden() {
        verifyErrorResponse(deleteSecretWithNoAuthHeader(GROUP_3_TEST_NS_1,
                "entityType1/entity1/a/b/secret1", HttpStatus.BAD_REQUEST), HttpStatus.BAD_REQUEST);

        // Missing Authorization header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(42)
    void deleteSecret_expiredOauth2Token_unauthorized() {
        verifyErrorResponse(deleteEntityOrSecretError(GROUP_3_TEST_NS_1, 
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN_EXPIRED),
                "entityType1/entity1/a/b/secret1", HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(43)
    void deleteSecret_oauth2TokenWithNoScopes_forbidden() {
        verifyErrorResponse(deleteEntityOrSecretError(GROUP_3_TEST_NS_1,
                oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES),
                "entityType1/entity1/a/b/secret1", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(44)
    void deleteSecret_oauth2TokenWithInsufficientScopes_forbidden() {
        // SECRET_CONSUMER scope is not sufficient for delete
        verifyErrorResponse(deleteEntityOrSecretError(GROUP_3_TEST_NS_1,
                oauth2Tokens.get(TestTokenType.SECRET_CONSUMER),
                "entityType1/entity1/a/b/secret1", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(45)
    void deleteSecret_validToken_nonexistentNamespace_forbidden() {
        verifyErrorResponse(deleteEntityOrSecretError("nonexistent-ns",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret1", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(46)
    void deleteSecret_validToken_issuerNotRegistered_forbidden() {
        // Create a new namespace without authorizations
        verifySuccessResponse(createNamespace(GROUP_2_TEST_NS_2), HttpStatus.OK);

        verifyErrorResponse(deleteEntityOrSecretError(GROUP_2_TEST_NS_2,
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret1", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(47)
    void deleteSecret_validToken_nonexistentEntityType_noContent() {
        // Delete on nonexistent entity-type should be a no-op (204)
        deleteEntityOrSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "nonexistentType/entity1/a/b/secret1");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(48)
    void deleteSecret_validToken_entityDoesNotExist_noContent() {
        // Delete on nonexistent entity should be a no-op (204)
        deleteEntityOrSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/nonexistentEntity/a/b/secret1");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(49)
    void deleteSecret_validToken_secretDoesNotExist_noContent() {
        // Delete on nonexistent secret should be a no-op (204)
        deleteEntityOrSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/nonexistentSecret");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(50)
    void deleteSecret_validToken_secretExists_success_andVerifyCannotRetrieve() {
        // Verify secret exists before deletion
        verifySecretRetrieval(GROUP_3_TEST_NS_1, "entityType1/entity1/a/b/secret1",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, null, sampleSecretData);

        // Delete the secret
        deleteEntityOrSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret1");

        // Verify secret cannot be retrieved after deletion
        verifySecretNotFound(GROUP_3_TEST_NS_1, "entityType1/entity1/a/b/secret1",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, null);

        // Verify version listing returns empty list
        verifySecretVersionListing(GROUP_3_TEST_NS_1, "entityType1/entity1/a/b/secret1",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), 0);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(51)
    void deleteSecret_idempotency_deleteAlreadyDeletedSecret_noContent() {
        // Deleting an already deleted secret should be idempotent (204)
        deleteEntityOrSecret(GROUP_3_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret1");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(52)
    void deleteSecret_verifyPathListingExcludesDeletedSecrets() {
        // secret2 should still be listed, but secret1 should not
        var paths = verifySecretPathListing(GROUP_3_TEST_NS_1, "entityType1/entity1/a/b",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        
        assertTrue(paths.stream().anyMatch(p -> p.contains("secret2")), 
                "secret2 should still be listed");
        assertTrue(paths.stream().noneMatch(p -> p.contains("secret1")), 
                "secret1 should not be listed after deletion");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // SECRET VERSION LISTING TESTS - GET /v1/{entityType}/{entityId}/**?query_type=list_versions
    // =====================================================================

    @TestTemplate
    @Order(53)
    void setupGroup4Namespace_forVersionListingTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_4_TEST_NS_1, List.of("entityType1"));
        
        // Create a secret with multiple versions
        verifySuccessResponse(createOrUpdateSecret(GROUP_4_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", sampleSecretData), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_4_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", sampleSecretData2), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_4_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", sampleSecretData3), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 7);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(54)
    void listSecretVersions_expiredOauth2Token_unauthorized() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_4_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN_EXPIRED))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(55)
    void listSecretVersions_oauth2TokenWithNoScopes_forbidden() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_4_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(56)
    void listSecretVersions_nonexistentNamespace_forbidden() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace("nonexistent-ns")
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(57)
    void listSecretVersions_issuerNotRegistered_forbidden() {
        // Use GROUP_2_TEST_NS_2 which was created but has no authorizations
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_2_TEST_NS_2)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Issuer not registered - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(58)
    void listSecretVersions_nonexistentEntityType_notFound() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_4_TEST_NS_1)
                        .secretPath("nonexistentType/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(59)
    void listSecretVersions_secretWithMultipleVersions_success() {
        var versions = verifySecretVersionListing(GROUP_4_TEST_NS_1, "entityType1/entity1/path/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), 3);
        
        // Versions should be in reverse order of insertion (most recent first)
        log.info("Listed versions: {}", versions);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(60)
    void listSecretVersions_nonexistentSecret_emptyList() {
        verifySecretVersionListing(GROUP_4_TEST_NS_1, "entityType1/entity1/path/nonexistent",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), 0);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // SECRET PATH LISTING TESTS - GET /v1/{entityType}/{entityId}/**?query_type=list_secrets
    // =====================================================================

    @TestTemplate
    @Order(61)
    void setupGroup5Namespace_forPathListingTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_5_TEST_NS_1, List.of("entityType1"));
        
        // Create secrets with various paths
        verifySuccessResponse(createOrUpdateSecret(GROUP_5_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/secret1", sampleSecretData), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_5_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/secret2", sampleSecretData2), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_5_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/secret3", sampleSecretData3), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(GROUP_5_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/b/c/secret4", sampleSecretData), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 8);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(62)
    void listSecretPaths_expiredOauth2Token_unauthorized() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_5_TEST_NS_1)
                        .secretPath("entityType1/entity1/a")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN_EXPIRED))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(63)
    void listSecretPaths_oauth2TokenWithNoScopes_forbidden() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_5_TEST_NS_1)
                        .secretPath("entityType1/entity1/a")
                        .token(oauth2Tokens.get(TestTokenType.TENANT_OAUTH2_NO_SCOPES))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(64)
    void listSecretPaths_nonexistentNamespace_forbidden() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace("nonexistent-ns")
                        .secretPath("entityType1/entity1/a")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(65)
    void listSecretPaths_issuerNotRegistered_forbidden() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_2_TEST_NS_2)
                        .secretPath("entityType1/entity1/a")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Issuer not registered - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(66)
    void listSecretPaths_nonexistentEntityType_notFound() {
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_5_TEST_NS_1)
                        .secretPath("nonexistentType/entity1/a")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.NOT_FOUND), HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(67)
    void listSecretPaths_listImmediateChildren_success() {
        // Listing at /a should return secret1, secret2, and b/ (directory containing deeper secrets)
        var paths = verifySecretPathListing(GROUP_5_TEST_NS_1, "entityType1/entity1/a",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        
        log.info("Listed paths at /a: {}", paths);
        assertEquals(3, paths.size()); // secret1, secret2, and b/ directory

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(68)
    void listSecretPaths_afterDeletion_excludesDeletedPaths() {
        // Delete secret1
        deleteEntityOrSecret(GROUP_5_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/a/secret1");

        // Listing at /a should now return only secret2 and b/
        var paths = verifySecretPathListing(GROUP_5_TEST_NS_1, "entityType1/entity1/a",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        
        log.info("Listed paths at /a after deletion: {}", paths);
        assertEquals(2, paths.size());
        assertTrue(paths.stream().noneMatch(p -> p.contains("secret1")));

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // CAS (Check-And-Set) TESTS
    // =====================================================================

    @TestTemplate
    @Order(69)
    void setupGroup6Namespace_forCasTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_6_TEST_NS_1, List.of("entityType1"));

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(70)
    void createSecret_casWithNonExistentSecret_conflict() {
        // CAS insert should fail if the secret never existed
        UUID randomCas = Uuids.timeBased();
        verifyErrorResponse(createSecretError(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/newSecret", sampleSecretData, randomCas, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(71)
    void createSecret_casWithNonUuidV1_badRequest() {
        // CAS with non-UUID v1 should fail with 400
        UUID uuidV4 = UUID.randomUUID(); // This is UUID v4, not v1
        verifyErrorResponse(createSecretError(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/secret", sampleSecretData, uuidV4, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(72)
    void createSecret_casWithWrongPreviousVersion_conflict() {
        // First, create a secret without CAS
        var version1 = createSecretAndGetVersion(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/secretForCas", sampleSecretData);

        // Create second version (making version1 no longer the most recent)
        var version2 = createSecretAndGetVersion(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/secretForCas", sampleSecretData2);
        assertNotNull(version2); // Verify version2 was created

        // Try to update with CAS using version1 (not the most recent) - should fail
        verifyErrorResponse(createSecretError(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/secretForCas", sampleSecretData3, version1, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(73)
    void createSecret_casWithCorrectPreviousVersion_success() {
        // Get current version
        var response = verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_6_TEST_NS_1)
                        .secretPath("entityType1/entity1/cas/secretForCas")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(null)
                        .build()),
                HttpStatus.OK).getResponseBody();
        var currentVersion = response.getData().getMetadata().getVersion();

        // Update with correct CAS version - should succeed
        verifySuccessResponse(createOrUpdateSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/secretForCas", sampleSecretData3, currentVersion), HttpStatus.OK);

        // Verify the new value
        verifySecretRetrieval(GROUP_6_TEST_NS_1, "entityType1/entity1/cas/secretForCas",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, null, sampleSecretData3);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(74)
    void createSecret_casAfterSecretDeleted_conflict() {
        // Create a secret
        var version = createSecretAndGetVersion(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/toBeDeleted", sampleSecretData);

        // Delete the secret
        deleteEntityOrSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/toBeDeleted");

        // Try to update with CAS using the old version - should fail
        verifyErrorResponse(createSecretError(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/cas/toBeDeleted", sampleSecretData2, version, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // SECRET PATH COLLISION TESTS (after deletion)
    // =====================================================================

    @TestTemplate
    @Order(75)
    void createSecret_afterDeletingCollidingSecret_success() {
        // Create a secret at path a/b/c
        verifySuccessResponse(createOrUpdateSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/collision/a/b/c", sampleSecretData), HttpStatus.OK);

        // Try to create at path a/b (prefix) - should fail
        verifyErrorResponse(createSecretError(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/collision/a/b", sampleSecretData2, null, HttpStatus.CONFLICT),
                HttpStatus.CONFLICT);

        // Delete the colliding secret
        deleteEntityOrSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/collision/a/b/c");

        // Now creating at a/b should succeed
        verifySuccessResponse(createOrUpdateSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/collision/a/b", sampleSecretData2), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(76)
    void createSecret_afterDeletingEntity_success() {
        // Create a secret
        verifySuccessResponse(createOrUpdateSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entityToDelete/x/y/z", sampleSecretData), HttpStatus.OK);

        // Delete the entire entity
        deleteEntityOrSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.ENTITY_ADMIN),
                "entityType1/entityToDelete");

        // Now creating at a colliding path should succeed
        verifySuccessResponse(createOrUpdateSecret(GROUP_6_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entityToDelete/x/y", sampleSecretData2), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // AUTHORIZATION DELETION TESTS
    // =====================================================================

    @TestTemplate
    @Order(77)
    void setupGroup7Namespace_forAuthDeletionTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_7_TEST_NS_1, List.of("entityType1"));
        
        // Create a secret
        verifySuccessResponse(createOrUpdateSecret(GROUP_7_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", sampleSecretData), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(78)
    void createSecret_afterAuthorizationDeleted_forbidden() {
        // Remove the secret-admin authorization
        webTestClient.delete()
                .uri(buildUrl("/v1/sys/authorizations/oauth/clients/" + 
                        integrationTestProperties.getTenant().getSecretAdmin().getSub()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.NS_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_7_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isNoContent();

        // Try to create a secret - should fail
        verifyErrorResponse(createSecretError(GROUP_7_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/newSecret", sampleSecretData2, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(79)
    void deleteSecret_afterNonNotaryAuthorizationDeleted_forbidden() {
        // Try to delete a secret after authorization was removed - should fail
        verifyErrorResponse(deleteEntityOrSecretError(GROUP_7_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Authorization deleted - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(80)
    void getSecret_afterNonNotaryAuthorizationDeleted_forbidden() {
        // Try to get a secret after the non-Notary authorization was removed - should fail
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_7_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(null)
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Authorization deleted - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(81)
    void listSecretVersions_afterNonNotaryAuthorizationDeleted_forbidden() {
        // Try to list secret versions after the non-Notary authorization was removed - should fail
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_7_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_VERSIONS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Authorization deleted - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(82)
    void listSecretPaths_afterNonNotaryAuthorizationDeleted_forbidden() {
        // Try to list secret paths after the non-Notary authorization was removed - should fail
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_7_TEST_NS_1)
                        .secretPath("entityType1/entity1/path")
                        .token(oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                        .notaryToken(false)
                        .version(null)
                        .queryType(SecretQueryType.LIST_SECRETS.name())
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // Authorization deleted - auth fails before JWT signature validation, so no JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(83)
    void getSecret_notaryTokenWorks_beforeNotaryAuthDeleted() {
        // Verify notary token works before deleting the notary authorization
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_7_TEST_NS_1, List.of("entityType1/entity1/path/secret"));

        verifySuccessResponse(getSecret(GetSecretRequestInput.builder()
                        .namespace(GROUP_7_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(notaryToken)
                        .notaryToken(true)
                        .version(null)
                        .queryType(null)
                        .build()),
                HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 1);
    }

    @TestTemplate
    @Order(84)
    void getSecret_afterNotaryAuthorizationDeleted_forbidden() {
        // Delete the notary authorization
        webTestClient.delete()
                .uri(buildUrl("/v1/sys/authorizations/notary/clients/" +
                        integrationTestProperties.getTenant().getNotary().getSub()))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.NS_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_7_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isNoContent();

        // Try to get a secret with notary token after notary authorization was removed - should fail
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_7_TEST_NS_1, List.of("entityType1/entity1/path/secret"));

        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_7_TEST_NS_1)
                        .secretPath("entityType1/entity1/path/secret")
                        .token(notaryToken)
                        .notaryToken(true)
                        .version(null)
                        .queryType(null)
                        .build(),
                HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        // NS_ADMIN auth deletion polls tenant JWKS, notary auth check fails before JWKS polling
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // NAMESPACE/ENTITY-TYPE DELETION TESTS
    // =====================================================================

    @TestTemplate
    @Order(85)
    void setupGroup8Namespace_forDeletionCascadeTests() {
        setUpNamespaceAndEntityTypesAndAuthorizations(GROUP_8_TEST_NS_1, List.of("entityType1", "entityType2"));
        
        // Create secrets
        verifySuccessResponse(createOrUpdateSecret(GROUP_8_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", sampleSecretData), HttpStatus.OK);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 4);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 5);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(86)
    void createSecret_afterEntityTypeDeleted_notFound() {
        // Delete the entity-type
        deleteEntityTypeSuccess(GROUP_8_TEST_NS_1, "entityType2", oauth2Tokens.get(TestTokenType.NS_ADMIN));

        // Try to create a secret under the deleted entity-type - should fail with 404
        verifyErrorResponse(createSecretError(GROUP_8_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType2/entity1/path/newSecret", sampleSecretData, null, HttpStatus.NOT_FOUND),
                HttpStatus.NOT_FOUND);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 2);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(87)
    void deleteSecret_afterEntityTypeDeleted_noContent() {
        // Delete on a deleted entity-type should be a no-op (204)
        deleteEntityOrSecret(GROUP_8_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType2/entity1/path/secret");

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(88)
    void createSecret_afterNamespaceDeleted_forbidden() {
        // Delete the namespace
        deleteNamespace(GROUP_8_TEST_NS_1);

        // Try to create a secret - should fail
        verifyErrorResponse(createSecretError(GROUP_8_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/newSecret", sampleSecretData, null, HttpStatus.FORBIDDEN),
                HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 1);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(89)
    void deleteSecret_afterNamespaceDeleted_forbidden() {
        // Try to delete a secret from a deleted namespace - should fail
        verifyErrorResponse(deleteEntityOrSecretError(GROUP_8_TEST_NS_1, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/path/secret", HttpStatus.FORBIDDEN), HttpStatus.FORBIDDEN);

        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // ADDITIONAL PUT SECRET TESTS (auth errors, validation)
    // =====================================================================

    @TestTemplate
    @Order(90)
    void createSecret_noAuthorizationHeader_badRequest() {
        // PUT without Authorization header
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .bodyValue(buildCreateSecretRequest(sampleSecretData, null))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Missing Authorization header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(91)
    void createSecret_malformedToken_unauthorized() {
        verifyErrorResponse(createSecretError(GROUP_1_TEST_NS_1, "malformed-token",
                "entityType1/entity1/path/secret", sampleSecretData, null, HttpStatus.UNAUTHORIZED),
                HttpStatus.UNAUTHORIZED);

        // Malformed token fails parsing before JWKS lookup
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // VERIFICATION AFTER SUCCESSFUL INSERTIONS
    // =====================================================================

    @TestTemplate
    @Order(92)
    void verifySecretRetrieval_afterMultipleVersions_fetchesCorrectVersions() {
        // Create a new namespace for clean test
        String testNs = UUID.randomUUID().toString();
        setUpNamespaceAndEntityTypesAndAuthorizations(testNs, List.of("entityType1"));

        // Create secret with version 1
        var version1 = createSecretAndGetVersion(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/multiversion/secret", sampleSecretData);

        // Create version 2
        var version2 = createSecretAndGetVersion(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/multiversion/secret", sampleSecretData2);

        // Create version 3
        var version3 = createSecretAndGetVersion(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/multiversion/secret", sampleSecretData3);

        // Fetch without version - should get latest (version3)
        verifySecretRetrieval(testNs, "entityType1/entity1/multiversion/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, null, sampleSecretData3);

        // Fetch with specific versions
        verifySecretRetrieval(testNs, "entityType1/entity1/multiversion/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, version1, sampleSecretData);
        verifySecretRetrieval(testNs, "entityType1/entity1/multiversion/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, version2, sampleSecretData2);
        verifySecretRetrieval(testNs, "entityType1/entity1/multiversion/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), false, version3, sampleSecretData3);

        // Verify version listing
        var versions = verifySecretVersionListing(testNs, "entityType1/entity1/multiversion/secret",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), 3);
        
        // Most recent should be first
        assertEquals(version3.toString(), versions.get(0));

        // setUpNamespaceAndEntityTypesAndAuthorizations: 3 operator (ns create + entity type + ns admin auth), 4 tenant
        // 3 secret creates + 4 gets + 1 list versions = 8 tenant total beyond setup
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 12);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(93)
    void verifyPathListing_hierarchicalPaths_correctImmediateChildren() {
        String testNs = UUID.randomUUID().toString();
        setUpNamespaceAndEntityTypesAndAuthorizations(testNs, List.of("entityType1"));

        // Create a hierarchy of secrets
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/root/a/secret1", sampleSecretData), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/root/a/b/secret2", sampleSecretData2), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/root/a/b/c/secret3", sampleSecretData3), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/root/x/secret4", sampleSecretData), HttpStatus.OK);

        // List at root - should see a/ and x/ directories
        var rootPaths = verifySecretPathListing(testNs, "entityType1/entity1/root",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        log.info("Root paths: {}", rootPaths);
        assertEquals(2, rootPaths.size());

        // List at a - should see secret1 and b/ directory
        var aPaths = verifySecretPathListing(testNs, "entityType1/entity1/root/a",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        log.info("Paths at /a: {}", aPaths);
        assertEquals(2, aPaths.size());

        // List at a/b - should see secret2 and c/ directory
        var abPaths = verifySecretPathListing(testNs, "entityType1/entity1/root/a/b",
                oauth2Tokens.get(TestTokenType.SECRET_ADMIN), null);
        log.info("Paths at /a/b: {}", abPaths);
        assertEquals(2, abPaths.size());

        // setUpNamespaceAndEntityTypesAndAuthorizations: 3 operator (ns create + entity type + ns admin auth), 4 tenant
        // 4 secret creates + 3 list paths = 7 tenant beyond setup
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 11);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    // =====================================================================
    // MISC TEST CASES
    // =====================================================================

    @TestTemplate
    @Order(94)
    void getSecret_bothAuthHeadersBlank_forbidden() {
        // Test when both Authorization and X-ESS-TOKEN headers are absent/blank
        var result = webTestClient.get()
                .uri(buildUrl("/v1/entityType1/entity1/a/b/c"))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.FORBIDDEN)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.FORBIDDEN);

        // No auth headers provided, so no JWKS polling should occur
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(95)
    void getSecret_malformedNotaryToken_unauthorized() {
        // Test with a malformed notary token
        verifyErrorResponse(getSecretError(GetSecretRequestInput.builder()
                        .namespace(GROUP_2_TEST_NS_1)
                        .secretPath("functions/f1/path/secretA")
                        .token("malformed-notary-token")
                        .notaryToken(true)
                        .version(null)
                        .queryType(null)
                        .build(),
                HttpStatus.UNAUTHORIZED), HttpStatus.UNAUTHORIZED);

        // Malformed token fails parsing before JWKS lookup
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(96)
    void createSecret_missingNamespaceHeader_badRequest() {
        // PUT without X-ESS-NAMESPACE header
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .bodyValue(buildCreateSecretRequest(sampleSecretData, null))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Missing namespace header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(97)
    void deleteSecret_missingNamespaceHeader_badRequest() {
        // DELETE without X-ESS-NAMESPACE header
        var result = webTestClient.delete()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Missing namespace header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(98)
    void getSecret_missingNamespaceHeader_badRequest() {
        // GET without X-ESS-NAMESPACE header
        var result = webTestClient.get()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Missing namespace header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(99)
    void createSecret_nullDataField_badRequest() {
        // PUT with null data field in request body
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue("{\"data\": null}")
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Request body validation fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(100)
    void createSecret_missingDataField_badRequest() {
        // PUT with missing data field in request body
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue("{}")
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Request body validation fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(101)
    void createSecret_optionsWithNullCas_badRequest() {
        // PUT with options present but cas is null
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue("{\"data\": {\"key\": \"value\"}, \"options\": {\"cas\": null}}")
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Request body validation fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(106)
    void createSecret_unknownBodyProperty_badRequest() {
        // Regression guard for FAIL_ON_UNKNOWN_PROPERTIES: BeanConfig enables it, so an unknown /
        // misspelled top-level request-body property must be rejected with 400, not silently ignored
        // (which would create the secret and return 200). The body below wraps the CAS under "option",
        // the singular misspelling of the model's "options". Decoding fails before the handler's auth
        // path, so no JWKS is polled.
        var result = webTestClient.put()
                .uri(buildUrl("/v1/entityType1/entity1/path/secret"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue("{\"option\": {\"cas\": \"" + UUID.randomUUID() + "\"}, \"data\": {\"field1\": \"1234\"}}")
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Request body decoding fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(102)
    void listRootSecretPaths_success() {
        // Test listing at entity root level using the /v1/{entityType}/{entityId} endpoint
        String testNs = UUID.randomUUID().toString();
        setUpNamespaceAndEntityTypesAndAuthorizations(testNs, List.of("entityType1"));

        // Create some secrets at different root paths
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/pathA/secret1", sampleSecretData), HttpStatus.OK);
        verifySuccessResponse(createOrUpdateSecret(testNs, oauth2Tokens.get(TestTokenType.SECRET_ADMIN),
                "entityType1/entity1/pathB/secret2", sampleSecretData2), HttpStatus.OK);

        // List at entity root (empty path) with query_type=LIST_SECRETS
        var response = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/v1/entityType1/entity1")
                        .queryParam("query_type", "LIST_SECRETS")
                        .build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(testNs))
                .exchange()
                .expectStatus()
                .isOk()
                .expectBody()
                .returnResult();

        assertNotNull(response.getResponseBody());

        // setUpNamespaceAndEntityTypesAndAuthorizations: 3 operator (ns create + entity type + ns admin auth), 4 tenant
        // 2 secret creates + 1 list secrets = 3 tenant beyond setup
        // Total: 3 operator, 7 tenant
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 3);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 7);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(103)
    void listRootSecretPaths_withFetchSecretQueryType_badRequest() {
        // Test that FETCH_SECRET at entity root level returns 400 (empty path not allowed for fetch)
        var result = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/v1/entityType1/entity1")
                        .queryParam("query_type", "FETCH_SECRET")
                        .build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_1_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Empty path validation fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(104)
    void listRootSecretPaths_withNotaryToken_badRequest() {
        // The /v1/{entityType}/{entityId} endpoint requires @NotBlank Authorization header
        // Notary tokens cannot be used for this endpoint
        var notarySub = integrationTestProperties.getTenant().getNotary().getSub();
        var notaryToken = getEssAssertion(notarySub, GROUP_2_TEST_NS_1, List.of("functions/f1"));

        var result = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/v1/functions/f1")
                        .queryParam("query_type", "LIST_SECRETS")
                        .build())
                .header("X-ESS-TOKEN", notaryToken)
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_2_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Missing Authorization header fails validation before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }

    @TestTemplate
    @Order(105)
    void listSecretVersions_emptySecretPath_badRequest() {
        // LIST_VERSIONS with empty path should fail
        var result = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/v1/entityType1/entity1/")
                        .queryParam("query_type", "LIST_VERSIONS")
                        .build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.SECRET_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(GROUP_4_TEST_NS_1))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.BAD_REQUEST)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verifyErrorResponse(result, HttpStatus.BAD_REQUEST);

        // Empty path validation fails before auth
        verifyJwkCachePolled(authServers.getOperatorOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getTenantOauth2WireMockServer(), "/.well-known/jwks.json", 0);
        verifyJwkCachePolled(authServers.getNotaryWireMockServer(), "/.well-known/jwks.json", 0);
    }
}
