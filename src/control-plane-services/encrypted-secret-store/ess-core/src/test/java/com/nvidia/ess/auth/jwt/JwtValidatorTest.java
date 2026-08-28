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
package com.nvidia.ess.auth.jwt;

import static com.nvidia.ess.constants.Constants.REDACTED;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.auth.NotaryProperties;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.utils.DateUtils;
import java.net.URI;
import java.net.URL;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

//TODO: Add more test cases for more coverage
@ExtendWith(MockitoExtension.class)
class JwtValidatorTest {

    @Mock
    private Jwt jwt;

    private JwtClaimValidator jwtValidator;

    @BeforeEach
    void setUp() {
        jwtValidator = new JwtClaimValidator(jwt);
    }

    @Test
    public void validateIssuer_whenIssuerDoesNotMatch_shouldReturnError() throws Exception {
        URL issuerInToken = URI.create("http://incorrect-issuer.com").toURL();
        when(jwt.getIssuer()).thenReturn(issuerInToken);

        String expectedClaim = "http://correct-issuer.com";
        Mono<JwtClaimValidator> result = jwtValidator.validateIssuer(expectedClaim);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof UnauthorizedException &&
                                throwable.getMessage().contains(expectedClaim) &&
                                throwable.getMessage().contains(issuerInToken.toString()))
                .verify();
    }

    @Test
    public void validateIssuer_whenIssuerMatches_shouldReturnJwtValidator() throws Exception {
        URL issuerInToken = URI.create("http://correct-issuer.com").toURL();
        when(jwt.getIssuer()).thenReturn(issuerInToken);

        Mono<JwtClaimValidator> result = jwtValidator.validateIssuer("http://correct-issuer.com");

        StepVerifier.create(result)
                .expectNext(jwtValidator)
                .verifyComplete();
    }

    @Test
    void validateSubject_whenSubjectDoesNotMatch_shouldReturnError() {
        String actualClaim = UUID.randomUUID().toString();
        when(jwt.getSubject()).thenReturn(actualClaim);

        String expectedClaim = UUID.randomUUID().toString();
        Mono<JwtClaimValidator> result = jwtValidator.validateSubject(expectedClaim);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof UnauthorizedException &&
                                !throwable.getMessage().contains(expectedClaim) &&
                                throwable.getMessage().contains(REDACTED) &&
                                throwable.getMessage().contains(actualClaim))
                .verify();
    }

    @Test
    void validateSubject_whenSubjectMatches_shouldReturnJwtValidator() {
        when(jwt.getSubject()).thenReturn("correct-subject");

        Mono<JwtClaimValidator> result = jwtValidator.validateSubject("correct-subject");

        StepVerifier.create(result)
                .expectNext(jwtValidator)
                .verifyComplete();
    }

    @Test
    void validateAud_whenAudienceDoesNotMatch_shouldReturnError() {
        String actualClaim = "incorrect-audience";
        when(jwt.getAudience()).thenReturn(Collections.singletonList(actualClaim));

        String expectedClaim = "correct-audience";
        Mono<JwtClaimValidator> result = jwtValidator.validateAud(expectedClaim);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof UnauthorizedException &&
                                throwable.getMessage().contains(expectedClaim) &&
                                throwable.getMessage().contains(actualClaim))
                .verify();
    }

    @Test
    void validateAud_whenAudienceMatches_shouldReturnJwtValidator() {
        when(jwt.getAudience()).thenReturn(Collections.singletonList("correct-audience"));

        Mono<JwtClaimValidator> result = jwtValidator.validateAud("correct-audience");

        StepVerifier.create(result)
                .expectNext(jwtValidator)
                .verifyComplete();
    }

    @Test
    void validateScopes_whenScopesDoNotMatch_shouldReturnError() {
        Map<String, Object> scopes_map = new HashMap<>();
        List<String> scopes_list = List.of("incorrect-scope");
        scopes_map.put("scopes", scopes_list);

        when(jwt.getClaims()).thenReturn(scopes_map);
        when(jwt.getClaimAsStringList("scopes")).thenReturn(scopes_list);

        String expectedClaim = "correct-scope";
        Mono<JwtClaimValidator> result = jwtValidator.validateScopes(new String[]{expectedClaim});

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException &&
                                throwable.getMessage().contains(expectedClaim))
                .verify();
    }

    @Test
    void validateScopes_whenScopesMatch_shouldReturnJwtValidator() {
        Map<String, Object> scopes_map = new HashMap<>();
        List<String> scopes_list = List.of("correct-scope");
        List<String> scopes = new ArrayList<>(scopes_list);
        scopes_map.put("scopes", scopes);

        when(jwt.getClaims()).thenReturn(scopes_map);
        when(jwt.getClaimAsStringList("scopes")).thenReturn(scopes_list);

        Mono<JwtClaimValidator> result = jwtValidator.validateScopes(new String[]{"correct-scope"});

        StepVerifier.create(result)
                .expectNext(jwtValidator)
                .verifyComplete();
    }

    @Test
    void validateAssertions_whenIatIsMissing_shouldReturnError() {
        when(jwt.getIssuedAt()).thenReturn(null);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", "secretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof UnauthorizedException &&
                                throwable.getMessage().contains(Constants.MSG_MISSING_IAT))
                .verify();
    }

    @Test
    void validateAssertions_whenAssertionsAreValid_shouldReturnJwtValidator() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        Map<String, Object> assertions_map = Map.of(
                "namespace", "namespace",
                "secretPaths", List.of("/dir1/dir2/secretPath1", "/dir1/dir2/secretPath2")
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", "/dir1/dir2/secretPath2", notaryProperties);

        StepVerifier.create(result)
                .expectNext(jwtValidator)
                .verifyComplete();
    }

    @Test
    void validateAssertions_whenNamespaceDoesNotMatch_shouldReturnForbiddenException() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        String actualNamespace = "incorrect_namespace";
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        Map<String, Object> assertions_map = Map.of(
                "namespace", actualNamespace,
                "secretPaths", List.of("secretPath")
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        String expectedNamespace = "namespace";
        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions(expectedNamespace, "secretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException &&
                                throwable.getMessage().contains(expectedNamespace) &&
                                throwable.getMessage().contains(actualNamespace))
                .verify();
    }

    @Test
    void validateAssertions_whenSecretPathsMismatch_shouldReturnForbiddenException() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        String actualSecretPath = "incorrect_secretPath";
        Map<String, Object> assertions_map = Map.of(
                "namespace", "namespace",
                "secretPaths", List.of(actualSecretPath)
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        String expectedSecretPath = "correctSecretPath";
        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", expectedSecretPath, notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException &&
                                throwable.getMessage().contains(expectedSecretPath) &&
                                throwable.getMessage().contains(actualSecretPath))
                .verify();
    }

    @Test
    void validateAssertions_whenSecretPathsIsNotList_shouldReturnForbiddenException() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        Object actualSecretPathsObject = "secretPath";
        Map<String, Object> assertions_map = Map.of(
                "namespace", "namespace",
                "secretPaths", actualSecretPathsObject
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", "correctSecretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException &&
                                throwable.getMessage().contains(actualSecretPathsObject.toString()))
                .verify();
    }

    @Test
    void validateAssertions_whenSecretPathsMissing_shouldReturnForbiddenException() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        Map<String, Object> assertions_map = Map.of(
                "namespace", "namespace"
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        String expectedSecretPath = "correctSecretPath";
        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", "correctSecretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException &&
                                throwable.getMessage().contains(expectedSecretPath))
                .verify();
    }

    @Test
    void validateAssertions_whenNamespaceIsNull_shouldReturnForbiddenException() {
        Instant issuedAt = Instant.now().minusSeconds(3600);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        Map<String, Object> assertions_map = Map.of(
                "namespace", "namespace"
        );
        when(jwt.getClaims()).thenReturn(Map.of(
                "assertion", assertions_map
        ));

        when(jwt.getClaimAsMap("assertion")).thenReturn(assertions_map);

        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));

        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions(null, "correctSecretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof ForbiddenException)
                .verify();
    }

    @Test
    void validateAssertions_whenAssertionExpired_shouldReturnUnauthorizedException() {
        Instant issuedAt = Instant.now().minusSeconds(5000);
        when(jwt.getIssuedAt()).thenReturn(issuedAt);
        NotaryProperties notaryProperties = new NotaryProperties();
        notaryProperties.setTtl(java.time.Duration.ofHours(1));
        notaryProperties.setClockSkewAdjustments(java.time.Duration.ofMinutes(5));
        long expiredAtEpoch = notaryProperties.getTtl().toMillis() +
                notaryProperties.getClockSkewAdjustments().toMillis() +
                issuedAt.getEpochSecond()*1000;

        Mono<JwtClaimValidator> result = jwtValidator.validateAssertions("namespace", "secretPath", notaryProperties);

        StepVerifier.create(result)
                .expectErrorMatches(throwable ->
                        throwable instanceof UnauthorizedException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_TOKEN_EXPIRED,
                                        DateUtils.epochToDateString(expiredAtEpoch))))
                .verify();
    }
}