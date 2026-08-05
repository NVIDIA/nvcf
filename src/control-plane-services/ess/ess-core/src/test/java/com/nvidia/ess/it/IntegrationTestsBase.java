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

import static com.github.tomakehurst.wiremock.client.WireMock.exactly;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.ess.auth.AuthProperties;
import com.nvidia.ess.auth.jwk.JwkSetService;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.CreateAuthorizationRequest;
import com.nvidia.ess.controller.request.CreateEntityTypeRequest;
import com.nvidia.ess.controller.request.CreateNamespaceRequest;
import com.nvidia.ess.controller.request.CreateSecretRequest;
import com.nvidia.ess.controller.request.CreateSecretRequest.Options;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.controller.response.NamespaceInfo;
import com.nvidia.ess.controller.response.kv2.CreateSecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretResponse;
import com.nvidia.ess.it.IntegrationTestProperties.OAuth2ClientProperties;
import com.nvidia.ess.it.IntegrationTestProperties.OperatorAuth;
import com.nvidia.ess.it.IntegrationTestProperties.TenantAuth;
import com.nvidia.ess.it.auth.AuthServers;
import com.nvidia.ess.it.multioauth.MultiOAuthMockServer;
import com.nvidia.ess.it.multioauth.OAuthTokenIssuerResponse;
import com.nvidia.ess.it.notary.model.AssertionRequest;
import com.nvidia.ess.it.notary.model.AssertionResponse;
import com.nvidia.ess.persistence.repositories.AuthorizationsRepository;
import java.util.Collection;
import java.util.EnumMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.Builder;
import lombok.Data;
import lombok.SneakyThrows;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.params.provider.Arguments;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.web.reactive.server.EntityExchangeResult;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.test.web.reactive.server.WebTestClient.ResponseSpec;
import org.springframework.web.reactive.function.BodyInserters;
import org.springframework.web.util.UriComponentsBuilder;

@AutoConfigureWebTestClient(timeout = "6000s")
class IntegrationTestsBase {

    static {
        System.setProperty("datastax-java-driver.advanced.request-tracker.class", "RequestLogger");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.success.enabled", "true");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.slow.enabled", "true");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.error.enabled", "true");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.show-values", "true");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.slow.threshold ", "1 second");
        System.setProperty("datastax-java-driver.advanced.request-tracker.logs.show-stack-trace", "true");
    }

    public static enum TestTokenType {
        OPERATOR,
        OPERATOR_EXPIRED,
        OPERATOR_NO_SCOPES,
        OPERATOR_WRONG_SCOPES,

        NS_ADMIN,
        NS_ADMIN_EXPIRED,

        ENTITY_ADMIN,
        ENTITY_ADMIN_EXPIRED,

        SECRET_ADMIN,
        SECRET_ADMIN_EXPIRED,

        SECRET_CONSUMER,
        SECRET_CONSUMER_EXPIRED,

        TENANT_OAUTH2_NO_SCOPES,

        NOTARY_SIGN_AUTH,
    }

    protected static final Map<TestTokenType, String> oauth2Tokens = new EnumMap<>(TestTokenType.class);

    protected static WebTestClient operatorWebTestClient;
    protected static WebTestClient tenantWebTestClient;
    protected static WebTestClient notarySignAuthWebTestClient;
    protected static WebTestClient notaryWebTestClient;

    protected static AuthServers authServers;

    @Autowired
    protected WebTestClient webTestClient;

    @Autowired
    protected IntegrationTestProperties integrationTestProperties;

    @Autowired
    protected AuthProperties authProperties;

    @MockitoSpyBean
    private JwkSetService jwkSetService;

    @Autowired
    protected AuthorizationsRepository authorizationsRepository;

    /**
     * The IT execution variant currently running. Maps a base namespace to a distinct namespace (via
     * {@link #effectiveNs(String)}), picks the {@code authorization.type} DB value, and dictates which
     * column(s) hold tenant (non-notary) auths. Defaults to the no-op {@link ITExecutionVariant#DEFAULT}
     * so ITs that do not opt in are unchanged.
     */
    protected ITExecutionVariant currentVariant = ITExecutionVariant.DEFAULT;

    /** Maps a base namespace to the namespace for the current execution variant. */
    protected String effectiveNs(String baseNamespace) {
        return currentVariant.effectiveNs(baseNamespace);
    }

    /** Crosses single-value arguments with every {@link AuthDbStateVariants} variant. */
    protected static Stream<Arguments> withAuthDbStateVariants(Collection<?> singleArgValues) {
        return crossVariants(AuthDbStateVariants.ALL, singleArgValues);
    }

    /** Crosses multi-value {@link Arguments} with every {@link AuthDbStateVariants} variant. */
    protected static Stream<Arguments> withAuthDbStateVariants(Stream<? extends Arguments> baseArgs) {
        return crossVariants(AuthDbStateVariants.ALL, baseArgs);
    }

    private static Stream<Arguments> crossVariants(List<ITExecutionVariant> variants, Collection<?> singleArgValues) {
        return variants.stream()
                .flatMap(variant -> singleArgValues.stream().map(value -> Arguments.of(variant, value)));
    }

    private static Stream<Arguments> crossVariants(List<ITExecutionVariant> variants,
            Stream<? extends Arguments> baseArgs) {
        var baseList = baseArgs.toList();
        return variants.stream().flatMap(variant -> baseList.stream().map(args -> {
            var original = args.get();
            var combined = new Object[original.length + 1];
            combined[0] = variant;
            System.arraycopy(original, 0, combined, 1, original.length);
            return Arguments.of(combined);
        }));
    }

    /**
     * Forces the just-registered non-notary authorization's {@code authorization.type} DB column to the
     * value dictated by the current variant. The add-auth API always persists {@code type = null} (via
     * the type-less model); this overwrites only the {@code type} column afterward so the read/auth
     * paths are exercised against {@code "OAUTH"} and random garbage values too.
     * {@code NULL} is left as the API wrote it, and notary auths are untouched.
     */
    protected void maybeOverwriteAuthTypeInDB(String baseNamespace, String subject, boolean isNotary) {
        if (isNotary) {
            return;
        }
        var typeInDb = currentVariant.nonNotaryAuthorizationTypeInDB().valueInDB();
        if (typeInDb == null) {
            return;
        }
        var namespace = effectiveNs(baseNamespace);
        authorizationsRepository.findById(namespace)
                .flatMap(model -> {
                    var udt = model.getOauthAuthorizations().get(subject);
                    return authorizationsRepository.overwriteNonNotaryAuthorizationWithType(namespace, subject,
                            udt.getId(), udt.getName(), udt.getJwksUrl(), udt.getIssuer(), typeInDb);
                })
                .block();
    }

    @BeforeAll
    static void beforeAll(@Autowired IntegrationTestProperties integrationTestProperties,
            @Autowired AuthProperties authProperties) {

        authServers = AuthServers.get(() -> AuthServers.CreationArgs.builder()
                .integrationTestProperties(integrationTestProperties)
                .authProperties(authProperties)
                .build());

        configureOperatorOauth2(integrationTestProperties.getOperator(), authServers.getOperatorOauth2MockServer(),
                authServers.getOperatorOauth2WireMockServer());
        configureTenantOauth2(integrationTestProperties.getTenant(),
                authServers.getTenantOauth2MockServer(),
                authServers.getTenantOauth2WireMockServer(),
                authServers.getNotarySignAuthOauth2MockServer(),
                authServers.getNotarySignAuthOauth2WireMockServer());
        configureTenantNotary(authServers.getNotaryWireMockServer());
    }

    @SneakyThrows
    static void configureOperatorOauth2(OperatorAuth operatorAuth,
            MultiOAuthMockServer operatorOauth2MockServer,
            WireMockServer operatorOauth2WireMockServer) {

        oauth2Tokens.put(TestTokenType.OPERATOR, operatorOauth2MockServer
                .getJwt(operatorAuth.getOauth2Client().getSub(),
                        List.of(AuthScope.ESS_OPERATOR), 3600, false));

        oauth2Tokens.put(TestTokenType.OPERATOR_EXPIRED,  operatorOauth2MockServer
                .getJwt(operatorAuth.getOauth2Client().getSub(),
                        List.of(AuthScope.ESS_OPERATOR), -3600, false));

        oauth2Tokens.put(TestTokenType.OPERATOR_NO_SCOPES, operatorOauth2MockServer
                .getJwt(operatorAuth.getOauth2Client().getSub(),
                        List.of(), 3600, false));

        oauth2Tokens.put(TestTokenType.OPERATOR_WRONG_SCOPES, operatorOauth2MockServer
                .getJwt(operatorAuth.getOauth2Client().getSub(),
                        List.of(AuthScope.ESS_NAMESPACE_ADMIN), 3600, true));

        operatorWebTestClient = WebTestClient.bindToServer()
                .baseUrl("http://localhost:" + operatorOauth2WireMockServer.port())
                .build();
    }

    @SneakyThrows
    static void configureTenantOauth2(TenantAuth tenantAuth,
            MultiOAuthMockServer tenantOauth2MockServer,
            WireMockServer tenantOauth2WireMockServer,
            MultiOAuthMockServer notarySignAuthMockServer,
            WireMockServer notarySignAuthWireMockServer) {

        oauth2Tokens.put(TestTokenType.NS_ADMIN, tenantOauth2MockServer
                .getJwt(tenantAuth.getNsAdmin().getSub(),
                        List.of(AuthScope.ESS_NAMESPACE_ADMIN), 3600, false));

        oauth2Tokens.put(TestTokenType.NS_ADMIN_EXPIRED, tenantOauth2MockServer
                .getJwt(tenantAuth.getNsAdmin().getSub(),
                        List.of(AuthScope.ESS_NAMESPACE_ADMIN), -3600, false));

        oauth2Tokens.put(TestTokenType.ENTITY_ADMIN, tenantOauth2MockServer
                .getJwt(tenantAuth.getEntityAdmin().getSub(),
                        List.of(AuthScope.ESS_ENTITIES_ADMIN), 3600, false));

        oauth2Tokens.put(TestTokenType.ENTITY_ADMIN_EXPIRED, tenantOauth2MockServer
                .getJwt(tenantAuth.getEntityAdmin().getSub(),
                        List.of(AuthScope.ESS_ENTITIES_ADMIN), -3600, false));

        oauth2Tokens.put(TestTokenType.SECRET_ADMIN, tenantOauth2MockServer
                .getJwt(tenantAuth.getSecretAdmin().getSub(),
                        List.of(AuthScope.ESS_SECRETS_ADMIN), 3600, false));

        oauth2Tokens.put(TestTokenType.SECRET_ADMIN_EXPIRED,  tenantOauth2MockServer
                .getJwt(tenantAuth.getSecretAdmin().getSub(),
                        List.of(AuthScope.ESS_SECRETS_ADMIN), -3600, false));

        oauth2Tokens.put(TestTokenType.SECRET_CONSUMER,  tenantOauth2MockServer
                .getJwt(tenantAuth.getSecretConsumer().getSub(), List.of(AuthScope.ESS_SECRETS_CONSUMER), 3600, false));

        oauth2Tokens.put(TestTokenType.SECRET_CONSUMER_EXPIRED,  tenantOauth2MockServer
                .getJwt(tenantAuth.getSecretConsumer().getSub(), List.of(AuthScope.ESS_SECRETS_CONSUMER), -3600, false));

        oauth2Tokens.put(TestTokenType.TENANT_OAUTH2_NO_SCOPES,  tenantOauth2MockServer
                .getJwt(tenantAuth.getNsAdmin().getSub(), List.of(), 3600, false));

        oauth2Tokens.put(TestTokenType.NOTARY_SIGN_AUTH, notarySignAuthMockServer
                .getJwt(tenantAuth.getNotarySignClient().getSub(), List.of("notary-sign"), 3600, false));

        tenantWebTestClient = WebTestClient.bindToServer()
                .baseUrl("http://localhost:" + tenantOauth2WireMockServer.port())
                .build();

        notarySignAuthWebTestClient = WebTestClient.bindToServer()
                .baseUrl("http://localhost:" + notarySignAuthWireMockServer.port())
                .build();
    }

    static void configureTenantNotary(WireMockServer notaryWireMockServer) {

        notaryWebTestClient = WebTestClient.bindToServer()
                .baseUrl("http://localhost:" + notaryWireMockServer.port())
                .build();
    }

    String getEssAssertion(String sub, String namespace, List<String> secretPaths) {
        return authServers.getNotaryMockServer().getJwt(sub, Map.of(
                "namespace", effectiveNs(namespace),
                "secretPaths", secretPaths
        ));
    }

    protected void verifyJwkCachePolled(WireMockServer jwksServer, String jwksUrl, int count) {

        // Verify that the look-through JWKs cache was polled the expected number of times within
        // the current test-case.
        var cacheKey = jwksServer.url(jwksUrl);
        verify(jwkSetService, times(count)).getJwkSet(cacheKey);

        if (count > 0) {
            // Verify that the JWKs service was polled once in order to populate the cache.
            jwksServer.verify(exactly(1), getRequestedFor(urlEqualTo(jwksUrl)));
        }
    }

    protected void setUpNamespaceAndEntityTypesAndAuthorizations(String namespace, List<String> entityTypes) {

        // Create the namespace.
        verifySuccessResponse(createNamespace(namespace), HttpStatus.OK);

        // Create the entity-types within the namespace.
        createEntityTypesInNamespace(namespace, entityTypes, true);

        // Register all authorizations.
        registerAllESSAuthorizations(namespace);
    }

    protected void createEntityTypesInNamespace(String namespace, List<String> entityTypes, boolean actorIsOperator) {
        for (String entityType : entityTypes) {
            verifySuccessResponse(createEntityType(namespace, entityType, actorIsOperator), HttpStatus.OK);
        }
    }

    protected void registerAllESSAuthorizations(String namespace) {

        verifySuccessResponse(addAuthorization(namespace, integrationTestProperties.getTenant().getNsAdmin().getIss(),
                integrationTestProperties.getTenant().getNsAdmin().getSub(), true,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(namespace, integrationTestProperties.getTenant().getEntityAdmin().getIss(),
                integrationTestProperties.getTenant().getEntityAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(namespace, integrationTestProperties.getTenant().getSecretAdmin().getIss(),
                integrationTestProperties.getTenant().getSecretAdmin().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(namespace, integrationTestProperties.getTenant().getSecretConsumer().getIss(),
                integrationTestProperties.getTenant().getSecretConsumer().getSub(), false,
                false), HttpStatus.OK);

        verifySuccessResponse(addAuthorization(namespace, integrationTestProperties.getTenant().getNotary().getIss(),
                integrationTestProperties.getTenant().getNotary().getSub(), false,
                true), HttpStatus.OK);
    }


    protected EntityExchangeResult<OAuthTokenIssuerResponse> getOperatorOauth2TokenWithApi() {
        return operatorWebTestClient.post()
                .uri(buildUrl("/token"))
                .header(HttpHeaders.AUTHORIZATION, BasicAuthHeaderFormatterFunction.getInstance()
                        .apply(integrationTestProperties.getOperator().getOauth2Client().getSub(),
                                integrationTestProperties.getOperator().getOauth2Client().getSecret()))
                .body(BodyInserters.fromFormData("grant_type", "client_credentials")
                        .with("scope", AuthScope.ESS_OPERATOR))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(OAuthTokenIssuerResponse.class)
                .returnResult();
    }


    protected EntityExchangeResult<OAuthTokenIssuerResponse> getTenantOauth2TokenWithApi(
            OAuth2ClientProperties client,
            List<String> authScopes) {

        return tenantWebTestClient.post()
                .uri(buildUrl("/token"))
                .header(HttpHeaders.AUTHORIZATION, BasicAuthHeaderFormatterFunction.getInstance()
                        .apply(client.getSub(), client.getSecret()))
                .body(BodyInserters.fromFormData("grant_type", "client_credentials")
                        .with("scope", StringUtils.join(authScopes, " ")))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(OAuthTokenIssuerResponse.class)
                .returnResult();
    }

    protected EntityExchangeResult<OAuthTokenIssuerResponse> getNotarySignAuthTokenWithApi() {
        return notarySignAuthWebTestClient.post()
                .uri(buildUrl("/token"))
                .header(HttpHeaders.AUTHORIZATION, BasicAuthHeaderFormatterFunction.getInstance()
                        .apply(integrationTestProperties.getTenant().getNotarySignClient().getSub(),
                                integrationTestProperties.getTenant().getNotarySignClient().getSecret()))
                .body(BodyInserters.fromFormData("grant_type", "client_credentials")
                        .with("scope", "notary-sign"))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(OAuthTokenIssuerResponse.class)
                .returnResult();
    }

    protected EntityExchangeResult<AssertionResponse> getTenantNotaryTokenWithApi(String oauth2Jwt, List<String> audienceServiceIds, Map<String, Object> data) {
        return notaryWebTestClient.post()
                .uri(buildUrl("/sign"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Jwt)
                .bodyValue(buildAssertionRequest(audienceServiceIds, data))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AssertionResponse.class)
                .returnResult();
    }

    protected EntityExchangeResult<ProblemDetail> getTenantNotaryTokenApiError(String oauth2Jwt, List<String> audienceServiceIds,
            Map<String, Object> data, HttpStatusCode errorCode) {
        return notaryWebTestClient.post()
                .uri(buildUrl("/sign"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Jwt)
                .bodyValue(buildAssertionRequest(audienceServiceIds, data))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    protected String fetchTenantNotaryToken(String namespace, List<String> secretPaths) {

        // Fetch an OAuth2 auth-token to authenticate to the notary-server's /sign endpoint (from the corresponding
        // OAuth2 service).
        var fetchedNotarySignAuthOauth2TokenResponse = verifySuccessResponse(getNotarySignAuthTokenWithApi(),
                HttpStatus.OK);
        var fetchedNotarySignAuthOauth2Token = fetchedNotarySignAuthOauth2TokenResponse.getResponseBody();

        // Notary assertion audiences and data.
        var notaryAssertionAudience = List.of(authProperties.getServiceId());
        var notaryAssertionData = Map.of("namespace", effectiveNs(namespace), "secretPaths", secretPaths);

        // Get a notary token from the notary server's /sign endpoint.
        var fetchedTenantNotaryTokenResponse = verifySuccessResponse(
                getTenantNotaryTokenWithApi(fetchedNotarySignAuthOauth2Token.getAccessToken(), notaryAssertionAudience,
                        notaryAssertionData),
                HttpStatus.OK);
        return fetchedTenantNotaryTokenResponse.getResponseBody().getAssertion();
    }

    protected EntityExchangeResult<NamespaceInfo> createNamespace(String namespace) {

        return webTestClient.post()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.OPERATOR))
                .bodyValue(buildCreateNamespaceRequest(effectiveNs(namespace)))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(NamespaceInfo.class)
                .returnResult();
    }

    protected EntityExchangeResult<Void> deleteNamespace(String namespace) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/namespaces/" + effectiveNs(namespace)))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Tokens.get(TestTokenType.OPERATOR))
                .exchange()
                .expectStatus()
                .isNoContent()
                .expectBody()
                .isEmpty();
    } 

    protected EntityExchangeResult<EntityTypeInfo> createEntityType(String namespace,
            String entityType, boolean isOperator) {
        return webTestClient.post()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION,
                        "Bearer " + oauth2Tokens.get(isOperator ? TestTokenType.OPERATOR : TestTokenType.NS_ADMIN))
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .bodyValue(buildEntityTypeRequest(entityType))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(EntityTypeInfo.class)
                .returnResult();
    }

    protected EntityExchangeResult<Void> deleteEntityTypeSuccess(String namespace, String entityType, String token) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .header(X_ESS_NAMESPACE_HEADER, effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isNoContent()
                .expectBody()
                .isEmpty();
    }

    /**
     * Registers an authorization through the public API and returns the response. 
     * For non-notary auths {@code /oauth/} endpoint is chosen by the current variant,
     * and the variant's legacy {@code authorization.type} DB value is maybe forced on afterward
     * depending on whether the {@link ITExecutionVariant} requires it.
     */
    protected EntityExchangeResult<AuthorizationInfo> addAuthorization(String namespace,
            String issuer, String subject, boolean isOperator,
            boolean isNotary) {

        String ns = effectiveNs(namespace);
        String url = isNotary
                ? "/v1/sys/authorizations/notary/clients"
                : "/v1/sys/authorizations/oauth/clients";
        var result = webTestClient.post()
                .uri(buildUrl(url))
                .header(HttpHeaders.AUTHORIZATION,
                        "Bearer " + oauth2Tokens.get(isOperator ? TestTokenType.OPERATOR : TestTokenType.NS_ADMIN))
                .header("X-ESS-NAMESPACE", ns)
                .bodyValue(buildCreateAuthorizationRequest(issuer, subject))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(AuthorizationInfo.class)
                .returnResult();

        // Force this variant's authorization.type DB value (no-op unless type != NULL).
        maybeOverwriteAuthTypeInDB(namespace, subject, isNotary);
        return result;
    }

    protected EntityExchangeResult<CreateSecretResponse> createOrUpdateSecret(String namespace, String oauth2Token, String secretPath, Map<String, Object> secretData) {
        return createOrUpdateSecret(namespace, oauth2Token, secretPath, secretData, null);
    }

    protected EntityExchangeResult<CreateSecretResponse> createOrUpdateSecret(String namespace, String oauth2Token, String secretPath, Map<String, Object> secretData, UUID cas) {
        return webTestClient.put()
                .uri(buildUrl("/v1/" + secretPath))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Token)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .bodyValue(buildCreateSecretRequest(secretData, cas))
                .exchange()
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(CreateSecretResponse.class)
                .returnResult();
    }

    protected EntityExchangeResult<Void> deleteEntityOrSecret(String namespace, String oauth2Token, String entityOrSecretPath) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/" + entityOrSecretPath))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Token)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.NO_CONTENT)
                .expectBody()
                .isEmpty();
    }

    protected EntityExchangeResult<ProblemDetail> deleteEntityOrSecretError(String namespace, String oauth2Token,
            String entityOrSecretPath, HttpStatusCode errorCode) {
        return webTestClient.delete()
                .uri(buildUrl("/v1/" + entityOrSecretPath))
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + oauth2Token)
                .header("X-ESS-NAMESPACE", effectiveNs(namespace))
                .exchange()
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    @Builder(toBuilder = true)
    @Data
    protected static class GetSecretRequestInput {
        private final String namespace;
        private final String secretPath;
        private final String token;
        private final boolean notaryToken;
        private final UUID version;
        private final String queryType;
    }

    private ResponseSpec performGetSecretRequest(GetSecretRequestInput input) {
        var builder = webTestClient.get();

        builder.uri(uriBuilder -> {

            uriBuilder = uriBuilder.path(buildUrl("/v1/" + input.getSecretPath()));

            if (!Objects.isNull(input.getVersion())) {
                uriBuilder = uriBuilder.queryParam("version", input.getVersion());
            }

            if (!Objects.isNull(input.getQueryType())) {
                uriBuilder = uriBuilder.queryParam("query_type", input.getQueryType());
            }

            return uriBuilder.build();
        });

        if (!input.isNotaryToken()) {
            builder.header(HttpHeaders.AUTHORIZATION, "Bearer " + input.getToken());
        } else {
            builder.header("X-ESS-TOKEN", input.getToken());
        }

        return builder
                .header("X-ESS-NAMESPACE", effectiveNs(input.getNamespace()))
                .exchange();
    }

    protected EntityExchangeResult<SecretResponse> getSecret(GetSecretRequestInput input) {
        return performGetSecretRequest(input)
                .expectStatus()
                .is2xxSuccessful()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();
    }

    protected EntityExchangeResult<ProblemDetail> getSecretError(GetSecretRequestInput input, HttpStatusCode errorCode) {
        return performGetSecretRequest(input)
                .expectStatus()
                .isEqualTo(errorCode)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    // chain method into verifying specific to the response body
    protected <T> EntityExchangeResult<T> verifySuccessResponse(
            EntityExchangeResult<T> response, HttpStatus expectedStatusCode,
            boolean checkForNonEmptyResponse) {
        assertEquals(expectedStatusCode, response.getStatus());

        if (checkForNonEmptyResponse) {
            var successResponse = response.getResponseBody();
            assertNotNull(successResponse);
        }

        return response;
    }

    protected <T> EntityExchangeResult<T> verifySuccessResponse(
            EntityExchangeResult<T> response, HttpStatus expectedStatusCode) {

        return verifySuccessResponse(response, expectedStatusCode,
                expectedStatusCode == HttpStatus.OK || expectedStatusCode == HttpStatus.ACCEPTED);
    }

    protected EntityExchangeResult<ProblemDetail> verifyErrorResponse(
            EntityExchangeResult<ProblemDetail> response,
            HttpStatus expectedErrorCode) {

        assertEquals(expectedErrorCode, response.getStatus());

        return response;
    }

    protected AssertionRequest buildAssertionRequest(List<String> audienceServiceIds, Map<String, Object> data) {
        return AssertionRequest.builder()
                .audienceServiceIds(audienceServiceIds)
                .data(data)
                .build();
    }

    protected CreateNamespaceRequest buildCreateNamespaceRequest(String namespace) {
        return CreateNamespaceRequest.builder()
                .namespace(namespace)
                .build();
    }


    protected CreateEntityTypeRequest buildEntityTypeRequest(String entityType) {
        return CreateEntityTypeRequest.builder()
                .name(entityType)
                .build();
    }


    protected CreateAuthorizationRequest buildCreateAuthorizationRequest(String issuer,
            String sub) {
        return CreateAuthorizationRequest.builder()
                .iss(issuer)
                .sub(sub)
                .name(String.format("authorization for %s in %s", sub, issuer))
                .build();
    }


    protected CreateSecretRequest buildCreateSecretRequest(Map<String, Object> data, UUID cas) {
        var builder = CreateSecretRequest.builder()
                .data(data);

        if (!Objects.isNull(cas)) {
            builder.options(Options.builder()
                    .cas(cas)
                    .build());
        }

        return builder.build();
    }


    protected String buildUrl(String urlPath) {
        return Strings.CS.removeEnd(UriComponentsBuilder.fromPath(urlPath)
                .encode()
                .toUriString(), "/");
    }

}
