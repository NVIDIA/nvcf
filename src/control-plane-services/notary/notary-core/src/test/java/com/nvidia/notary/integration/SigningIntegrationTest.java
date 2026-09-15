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
package com.nvidia.notary.integration;


import static com.nimbusds.jose.JWSAlgorithm.ES256;
import static com.nvidia.notary.utils.TestData.REQUEST_DATA_SERIALIZED;
import static com.nvidia.notary.utils.TestData.SERVICE_ID_1;
import static com.nvidia.notary.utils.TestData.TEST_FIXED_JTI;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.InstanceOfAssertFactories.LONG;
import static org.assertj.core.api.InstanceOfAssertFactories.STRING;
import static org.mockito.Mockito.when;
import static org.springframework.http.HttpMethod.POST;

import com.nimbusds.jose.JWSVerifier;
import com.nimbusds.jose.crypto.ECDSAVerifier;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.source.ImmutableJWKSet;
import com.nimbusds.jose.jwk.source.JWKSource;
import com.nimbusds.jose.proc.JWSVerificationKeySelector;
import com.nimbusds.jose.proc.SecurityContext;
import com.nimbusds.jwt.SignedJWT;
import com.nimbusds.jwt.proc.ConfigurableJWTProcessor;
import com.nimbusds.jwt.proc.DefaultJWTProcessor;
import com.nvidia.notary.NotaryTestApp;
import com.nvidia.notary.config.TestClockConfiguration;
import com.nvidia.notary.utils.TestClock;
import com.nvidia.notary.web.dto.AssertionResponse;
import jakarta.validation.constraints.NotNull;
import java.security.interfaces.ECPublicKey;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;

@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NotaryTestApp.class, TestClockConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active:integrationtest")
class SigningIntegrationTest extends BaseIntegrationTest {

    @AfterEach
    void cleanup() {
        TestClock.resetToDefaults();
    }

    @Test
    void signRejected_invalidScope() {
        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "invalid-scope", oauth2Issuer, Instant.now());
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertThat(result.getHeaders().get("WWW-Authenticate")).containsExactly(
                """
                        Bearer error="insufficient_scope", \
                        error_description="The request requires higher privileges than provided by the access token.", \
                        error_uri="https://tools.ietf.org/html/rfc6750#section-3.1"\
                        """);
    }

    @Test
    void signRejected_expiredToken() {
        Instant issuedAt = Instant.now().minus(1, ChronoUnit.DAYS);
        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "invalid-scope", oauth2Issuer, issuedAt);
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        List<String> responseHeaders = result.getHeaders().get("WWW-Authenticate");
        assertThat(responseHeaders).hasSize(1);
        assertThat(responseHeaders.getFirst()).matches(
                """
                        Bearer error="invalid_token", \
                        error_description="An error occurred while attempting to decode the Jwt: Jwt expired at \\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z", \
                        error_uri="https://tools.ietf.org/html/rfc6750#section-3.1".*\
                        """);
    }

    @Test
    void signRejected_invalidIssuer() {
        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "notary-test-sign",
                "http://invalid-service-id.localhost.local:8081",
                Instant.now());
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(result.getHeaders().get("WWW-Authenticate"))
                .hasSize(1)
                .first(STRING)
                .startsWith("Bearer error=\"invalid_token\", "
                            + "error_description=\"An error occurred while "
                            + "attempting to decode the Jwt: The iss claim is not valid\", "
                            + "error_uri=\"https://tools.ietf.org/html/rfc6750#section-3.1\"");
    }

    @Test
    void signRejected_audienceMissingBinding() {
        // Token is otherwise valid (correct issuer, valid scope, unexpired, signed by the mock),
        // but its audience does NOT contain any entry from notary.required-audiences.
        // This should be rejected by the JwtClaimValidator on the aud claim.
        Instant now = Instant.now();
        String token = getAccessTokenWithAudiences(
                List.of("notary-test-sign"),
                "oauth2-client-id",
                oauth2Issuer,
                now,
                List.of("s:some-other-service"));
        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        headers.add("Authorization", "Bearer " + token);
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(result.getHeaders().get("WWW-Authenticate"))
                .first(STRING)
                .contains("error=\"invalid_token\"");
    }

    @Test
    void signRejected_tokenWithoutSignature() {
        String tokenWithoutSignature = """
                eyJraWQiOiJiNmEwMWMwMy00ZjM4LTQxY2YtYTA3NC0zYTM0NWYyNzRkMTAiLCJhbGciOiJFUzI1NiJ9.\
                eyJzdWIiOiJzc2EtY2xpZW50LWlkIiwiYXVkIjpbInM6eHFsbmhuZno2dGpjei1hc2h1ZG9haGRrejZ6c\
                nBqdHR3aGF4amhha2V0bSIsInNzYS1jbGllbnQtaWQiLCJzOmxvY2FsaG9zdCJdLCJzZXJ2aWNlIjp7Im\
                5hbWUiOiJhY3Rvci1zZXJ2aWNlLW5hbWUiLCJpZCI6InhxbG5obmZ6NnRqY3otYXNodWRvYWhka3o2enJ\
                wanR0d2hheGpoYWtldG0ifSwiYXpwIjoic3NhLWNsaWVudC1pZCIsImlzcyI6Imh0dHA6Ly9sb2NhbGhv\
                c3Q6ODA4MSIsInNjb3BlcyI6WyJub3RhcnktdGVzdC1zaWduIl0sImV4cCI6MTY5NjMyMjQyNCwidG9rZ\
                W5fdHlwZSI6InNlcnZpY2VfYWNjb3VudCIsImlhdCI6MTY5NjMyMTUxNH0.""";

        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        headers.setAccept(List.of(MediaType.APPLICATION_JSON, MediaType.APPLICATION_PROBLEM_JSON));
        headers.add("Authorization", "Bearer " + tokenWithoutSignature);
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);
        var result = restTemplate.exchange("/sign", POST, request, String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(result.getHeaders().get("WWW-Authenticate"))
                .hasSize(1)
                .first(STRING)
                .startsWith("""
                        Bearer error="invalid_token", \
                        error_description="An error occurred while \
                        attempting to decode the Jwt: Malformed token", \
                        error_uri="https://tools.ietf.org/html/rfc6750#section-3.1"\
                        """);
    }

    @Test
    void shouldErrorIfJsonInvalid() {
        when(jtiGeneratorMock.generate()).thenReturn(TEST_FIXED_JTI);

        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "notary-test-sign", oauth2Issuer, Instant.now());

        HttpEntity<String> request = new HttpEntity<>("invalid-input:", headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(result.getBody()).isEqualTo(
                """
                        {\
                        "detail":"Failed to read request",\
                        "instance":"/sign",\
                        "status":400,\
                        "title":"Bad Request"}\
                        """);
    }

    @Test
    void shouldErrorIfRequestDataInvalid() {
        when(jtiGeneratorMock.generate()).thenReturn(TEST_FIXED_JTI);

        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "notary-test-sign", oauth2Issuer, Instant.now());

        String requestBody = """
                {
                    "audience_service_ids": ["xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm"],
                    "data": {}
                }
                """;
        HttpEntity<String> request = new HttpEntity<>(requestBody, headers);

        var result = restTemplate.exchange("/sign", POST, request, String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(result.getBody()).isEqualTo(
                """
                        {\
                        "detail":"Request data is empty",\
                        "instance":"/sign",\
                        "status":400,\
                        "title":"Bad Request",\
                        "type":"urn:nv-boot:problem-details:bad-request"}\
                        """);

    }

    @Test
    void shouldSignAssertion() {
        when(jtiGeneratorMock.generate()).thenReturn(TEST_FIXED_JTI);

        HttpHeaders headers = getRequestHeadersWithTokenForScope(
                "notary-test-sign",
                oauth2Issuer,
                Instant.now());
        HttpEntity<String> request = new HttpEntity<>(REQUEST_DATA_SERIALIZED, headers);

        var result = restTemplate.exchange("/sign", POST, request, AssertionResponse.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        String assertion = Objects.requireNonNull(result.getBody()).getAssertion();
        var parsedToken = getValidatedToken(assertion);
        Map<String, Object> claim = parsedToken.getPayload().toJSONObject();
        assertThat(claim)
                .containsEntry("iss", "http://assertion.issuer.test")
                .containsEntry("sub", "oauth2-client-id")
                .containsEntry("aud", SERVICE_ID_1)
                .containsEntry("jti", TEST_FIXED_JTI)
                .extractingByKey("iat", LONG)   // narrows the assertion type to Long
                .isNotNull();


        assertThat(claim.get("assertion")).hasToString(
                """
                        {string=Example String, number=123, boolean=true, null=null, \
                        object={nestedString=Nested Example, nestedNumber=456, \
                        nestedObject={deepString=Deeply Nested Example, deepArray=[7, 8, 9], \
                        deepObject={deeperString=Deeper Level, deeperNumber=101112, \
                        deeperObject={deepestString=Deepest Level, deepestList=[10, eleven, true]}}}}, \
                        array=[1, two, false, null, {objectInArray=789}], \
                        arrayOfObjects=[{id=1, name=Item One}, {id=2, name=Item Two}], \
                        specialCharacters=<>&"'/, escapedCharacters=\\t\\n\\r\\b\\f\\"\\\\}""");
    }

    private @NotNull HttpHeaders getRequestHeadersWithTokenForScope(
            String scope, String issuer, Instant issuedAt) {
        String token = getAccessToken(List.of(scope), "oauth2-client-id", issuer, issuedAt);
        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        headers.add("Authorization", "Bearer " + token);
        headers.add("Content-Type", "application/json");
        return headers;
    }

    @SneakyThrows
    private SignedJWT getValidatedToken(String signedTokenString) {
        String jwkSet = getNotaryServicePublicKeys();
        JWKSet jwks = JWKSet.parse(jwkSet);
        JWKSource<SecurityContext> keySource = new ImmutableJWKSet<>(jwks.toPublicJWKSet());
        ConfigurableJWTProcessor<SecurityContext> jwtProcessor = new DefaultJWTProcessor<>();
        jwtProcessor.setJWSKeySelector(new JWSVerificationKeySelector<>(ES256, keySource));
        SignedJWT signedJWT = SignedJWT.parse(signedTokenString);
        JWK jwk = jwks.getKeyByKeyId(signedJWT.getHeader().getKeyID());
        ECPublicKey ecPublicKey = ((ECKey) jwk).toECPublicKey();
        JWSVerifier verifier = new ECDSAVerifier(ecPublicKey);
        assertThat(signedJWT.verify(verifier)).isTrue();
        return signedJWT;
    }

}
