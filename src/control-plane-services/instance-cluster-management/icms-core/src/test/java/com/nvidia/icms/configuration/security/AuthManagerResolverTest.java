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
package com.nvidia.icms.configuration.security;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.outbound.apikeys.ApiKeysService;
import jakarta.servlet.http.HttpServletRequest;
import java.time.Instant;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import java.security.KeyPairGenerator;
import java.security.interfaces.RSAPrivateKey;
import java.security.interfaces.RSAPublicKey;
import java.util.Date;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidatorResult;
import org.springframework.security.oauth2.jwt.BadJwtException;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;

@ExtendWith(MockitoExtension.class)
class AuthManagerResolverTest {

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private ApiKeysService apiKeysService;

    private AuthManagerResolver resolver;

    @BeforeEach
    void setUp() {
        // Existing tests were written against pre-flag behavior (OIDC cluster identity active),
        // so enable the feature flag for all of them. A separate test exercises the flag-off path.
        NvcaConfigurationProperties nvcaConfig = new NvcaConfigurationProperties();
        nvcaConfig.setOidcClusterIdentityEnabled(true);
        resolver = new AuthManagerResolver(
                apiKeysService,
                clusterRepository,
                nvcaConfig,
                "ES256",
                "https://kubernetes.default.svc",
                "https://kubernetes.default.svc/openid/v1/jwks",
                "",
                "",
                new TrustedJwtIssuerProperties(),
                /* apiKeyAuthEnabled */ true);
    }

    // --- extractIssuerFromToken tests ---

    @Test
    void extractIssuerFromToken_validToken_returnsIssuer() {
        String payload = "{\"iss\":\"https://kubernetes.default.svc\",\"sub\":\"system:serviceaccount:test:sa\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String issuer = AuthManagerResolver.extractIssuerFromToken(token);
        assertEquals("https://kubernetes.default.svc", issuer);
    }

    @Test
    void extractIssuerFromToken_acceptsCaseInsensitiveBearerPrefix() {
        String payload = "{\"iss\":\"https://kubernetes.default.svc\",\"sub\":\"system:serviceaccount:test:sa\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "bEaReR eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String issuer = AuthManagerResolver.extractIssuerFromToken(token);
        assertEquals("https://kubernetes.default.svc", issuer);
    }

    @Test
    void extractIssuerFromToken_nullHeader_returnsNull() {
        assertNull(AuthManagerResolver.extractIssuerFromToken(null));
    }

    @Test
    void extractIssuerFromToken_noBearerPrefix_returnsNull() {
        assertNull(AuthManagerResolver.extractIssuerFromToken("Basic abc123"));
    }

    @Test
    void extractIssuerFromToken_malformedToken_returnsNull() {
        assertNull(AuthManagerResolver.extractIssuerFromToken("Bearer not-a-jwt"));
    }

    @Test
    void extractIssuerFromToken_rejectsOversizedJWT() {
        String oversizedPayload = "a".repeat(3000);
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + oversizedPayload + ".signature";
        assertNull(AuthManagerResolver.extractIssuerFromToken(token));
    }

    @Test
    void extractIssuerFromToken_noIssClaim_returnsNull() {
        String payload = "{\"sub\":\"test\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        assertNull(AuthManagerResolver.extractIssuerFromToken(token));
    }

    // --- extractClusterIdFromAudience tests ---

    @Test
    void extractClusterIdFromAudience_newFormat_returnsClusterId() {
        String payload = "{\"aud\":\"nvcf-icms:abc-123\",\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertEquals("abc-123", clusterId);
    }

    @Test
    void extractClusterIdFromAudience_acceptsCaseInsensitiveBearerPrefix() {
        String payload = "{\"aud\":\"nvcf-icms:abc-123\",\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "bEaReR eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertEquals("abc-123", clusterId);
    }

    @Test
    void extractClusterIdFromAudience_legacyFormat_returnsNull() {
        String payload = "{\"aud\":\"nvcf-icms\",\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_emptyClusterSuffix_returnsNull() {
        String payload = "{\"aud\":\"nvcf-icms:\",\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_blankClusterSuffix_returnsNull() {
        String payload = "{\"aud\":\"nvcf-icms:   \",\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_arrayFormat_returnsClusterId() {
        String payload = "{\"aud\":[\"other-audience\",\"nvcf-icms:abc-123\"],\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertEquals("abc-123", clusterId);
    }

    @Test
    void extractClusterIdFromAudience_arrayWithDifferentClusterAudiences_returnsNull() {
        String payload = "{\"aud\":[\"nvcf-icms:cluster-a\",\"nvcf-icms:cluster-b\"],\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_arrayWithRepeatedSameClusterAudience_returnsClusterId() {
        String payload = "{\"aud\":[\"nvcf-icms:abc-123\",\"other\",\"nvcf-icms:abc-123\"],\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertEquals("abc-123", clusterId);
    }

    @Test
    void extractClusterIdFromAudience_arrayWithLegacyOnly_returnsNull() {
        String payload = "{\"aud\":[\"nvcf-icms\",\"other\"],\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_noAudClaim_returnsNull() {
        String payload = "{\"iss\":\"https://k8s.example.com\"}";
        String encodedPayload = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        String token = "Bearer eyJhbGciOiJSUzI1NiJ9." + encodedPayload + ".signature";

        String clusterId = AuthManagerResolver.extractClusterIdFromAudience(token);
        assertNull(clusterId);
    }

    @Test
    void extractClusterIdFromAudience_nullHeader_returnsNull() {
        assertNull(AuthManagerResolver.extractClusterIdFromAudience(null));
    }

    @Test
    void extractClusterIdFromAudience_noBearerPrefix_returnsNull() {
        assertNull(AuthManagerResolver.extractClusterIdFromAudience("Basic abc123"));
    }

    // --- lookupClusterById tests ---

    @Test
    void lookupClusterById_existingCluster_returnsCluster() {
        ClusterEntity cluster = new ClusterEntity();
        cluster.setClusterId("cluster-1");

        when(clusterRepository.getClusterInfoByClusterId("cluster-1", false))
                .thenReturn(java.util.Optional.of(cluster));

        ClusterEntity result = resolver.lookupClusterById("cluster-1");
        assertNotNull(result);
        assertEquals("cluster-1", result.getClusterId());
    }

    @Test
    void lookupClusterById_nonExistent_returnsNull() {
        when(clusterRepository.getClusterInfoByClusterId("missing", false))
                .thenReturn(java.util.Optional.empty());

        assertNull(resolver.lookupClusterById("missing"));
    }

    @Test
    void lookupClusterById_nullOrBlank_returnsNull() {
        assertNull(resolver.lookupClusterById(null));
        assertNull(resolver.lookupClusterById(""));
    }

    // --- NVCA audience validator tests ---

    @Test
    void nvcaAudienceValidator_rejectsLegacyBareAudience() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("nvcf-icms"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected bare nvcf-icms audience to be rejected");
    }

    @Test
    void nvcaAudienceValidator_rejectsEmptyClusterSuffix() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("nvcf-icms:"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected empty nvcf-icms: audience suffix to be rejected");
    }

    @Test
    void nvcaAudienceValidator_rejectsBlankClusterSuffix() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("nvcf-icms:   "));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected blank nvcf-icms: audience suffix to be rejected");
    }

    @Test
    void nvcaAudienceValidator_acceptsNewFormatAudience() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("nvcf-icms:abc-123"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertFalse(result.hasErrors(), "Expected new format audience to be accepted");
    }

    @Test
    void nvcaAudienceValidator_rejectsUnknownAudience() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("some-other-service"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected unknown audience to be rejected");
    }

    @Test
    void nvcaAudienceValidator_acceptsMixedAudienceWithValidEntry() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("other", "nvcf-icms:cluster-42"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertFalse(result.hasErrors(), "Expected mixed audience with valid entry to be accepted");
    }

    @Test
    void nvcaAudienceValidator_rejectsMultipleDifferentClusterAudiences() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaAudienceValidator();
        Jwt jwt = buildJwtWithAudience(List.of("nvcf-icms:cluster-1", "nvcf-icms:cluster-2"));
        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected multiple different nvcf-icms audiences to be rejected");
    }

    private static Jwt buildJwtWithAudience(List<String> audiences) {
        Map<String, Object> headers = Map.of("alg", "RS256");
        Map<String, Object> claims = new HashMap<>();
        claims.put("iss", "https://kubernetes.default.svc");
        claims.put("sub", "system:serviceaccount:nvca-system:nvca");
        claims.put("aud", audiences);
        return new Jwt("token-value", Instant.now(), Instant.now().plusSeconds(3600),
                headers, claims);
    }

    // --- NVCA subject validator tests ---

    private static Jwt buildJwtWithSubject(String subject) {
        Map<String, Object> headers = Map.of("alg", "RS256");
        Map<String, Object> claims = new HashMap<>();
        claims.put("iss", "https://kubernetes.default.svc");
        if (subject != null) {
            claims.put("sub", subject);
        }
        return new Jwt("token-value", Instant.now(), Instant.now().plusSeconds(3600),
                headers, claims);
    }

    @Test
    void nvcaSubjectValidator_acceptsPsatNvcaSubject() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject("system:serviceaccount:nvca-system:nvca");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertFalse(result.hasErrors(), "Expected PSAT NVCA subject to be accepted");
    }

    @Test
    void nvcaSubjectValidator_acceptsNvcaSAInAnyNamespace() {
        // SA name must be exactly "nvca"; namespace is customer-configurable.
        // Verify the default helm namespace plus a customized one both pass.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();

        assertFalse(validator.validate(buildJwtWithSubject("system:serviceaccount:nvca-system:nvca")).hasErrors());
        assertFalse(validator.validate(buildJwtWithSubject("system:serviceaccount:customer-ns:nvca")).hasErrors());
        assertFalse(validator.validate(buildJwtWithSubject("system:serviceaccount:kube-system:nvca")).hasErrors());
    }

    @Test
    void nvcaSubjectValidator_rejectsWrongSAName() {
        // Any SA other than `nvca` must be rejected, regardless of namespace.
        // This closes the "any workload in the cluster can impersonate" gap
        // surfaced by the adversarial review.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();

        for (String bad : new String[] {
                "system:serviceaccount:nvca-system:nvca-operator",   // sibling SA
                "system:serviceaccount:nvca-system:nvca-attacker",   // prefix attack
                "system:serviceaccount:nvca-system:worker",          // unrelated SA
                "system:serviceaccount:default:attacker",            // arbitrary SA
                "system:serviceaccount:default:default",             // default SA
        }) {
            OAuth2TokenValidatorResult result = validator.validate(buildJwtWithSubject(bad));
            assertTrue(result.hasErrors(), "Expected rejection for " + bad);
            assertTrue(result.getErrors().iterator().next().getDescription()
                    .contains("Unauthorized subject"), "Error for " + bad);
        }
    }

    @Test
    void nvcaSubjectValidator_rejectsMalformedPsatSubjects() {
        // Subjects that start with system:serviceaccount: but aren't the
        // "<ns>:nvca" shape (missing ns, empty ns, extra ':' segments) must fail.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();

        for (String bad : new String[] {
                "system:serviceaccount:nvca",                   // missing namespace
                "system:serviceaccount::nvca",                  // empty namespace
                "system:serviceaccount:ns:nvca:extra",          // trailing segment
                "system:serviceaccount:",                       // just the prefix
        }) {
            assertTrue(validator.validate(buildJwtWithSubject(bad)).hasErrors(),
                    "Expected rejection for malformed subject " + bad);
        }
    }

    @Test
    void nvcaSubjectValidator_rejectsNonServiceAccountPrefix() {
        // Subjects that LOOK like a PSAT SA but don't have the exact prefix must be rejected.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject("system:user:default:attacker");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(),
                "Subject without system:serviceaccount: prefix must be rejected");
        assertTrue(result.getErrors().iterator().next().getDescription()
                .contains("Unauthorized subject"));
    }

    @Test
    void nvcaSubjectValidator_rejectsEmptySubject() {
        // Empty string doesn't match either PSAT or SPIFFE prefix.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject("");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Empty subject must be rejected");
    }

    @Test
    void nvcaSubjectValidator_acceptsSpiffeNvcaSubject() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject(
                "spiffe://trust-domain.example/tenant/nca-12345/cluster/abc-def-ghi/nvcf/nvca");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertFalse(result.hasErrors(), "Expected SPIFFE NVCA subject to be accepted");
    }

    @Test
    void nvcaSubjectValidator_rejectsWrongSpiffeSubject() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject(
                "spiffe://trust-domain.example/tenant/nca-12345/cluster/abc-def-ghi/nvcf/other-workload");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected wrong SPIFFE subject to be rejected");
        assertTrue(result.getErrors().iterator().next().getDescription().contains("Unauthorized subject"));
    }

    @Test
    void nvcaSubjectValidator_rejectsSpiffeNvcaPrefixAttack() {
        // `/nvcf/nvca-attacker` starts with the expected segment but is a
        // different workload. contains() would accept it; endsWith() must not.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject(
                "spiffe://trust-domain.example/tenant/nca-12345/cluster/abc-def-ghi/nvcf/nvca-attacker");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected SPIFFE subject with nvca-prefix segment to be rejected");
    }

    @Test
    void nvcaSubjectValidator_rejectsSpiffeNvcaTrailingPath() {
        // `/nvcf/nvca/malicious` contains the expected segment but with additional
        // trailing path components. contains() would accept it; endsWith() must not.
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject(
                "spiffe://trust-domain.example/tenant/nca-12345/cluster/abc-def-ghi/nvcf/nvca/malicious");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected SPIFFE subject with trailing path after nvca segment to be rejected");
    }

    @Test
    void nvcaSubjectValidator_rejectsNullSubject() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject(null);

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected null subject to be rejected");
        assertTrue(result.getErrors().iterator().next().getDescription().contains("Missing sub claim"));
    }

    @Test
    void nvcaSubjectValidator_rejectsArbitrarySubject() {
        OAuth2TokenValidator<Jwt> validator = AuthManagerResolver.nvcaSubjectValidator();
        Jwt jwt = buildJwtWithSubject("some-random-subject");

        OAuth2TokenValidatorResult result = validator.validate(jwt);
        assertTrue(result.hasErrors(), "Expected arbitrary subject to be rejected");
    }

    // --- buildJwtDecoderFromJwks tests ---

    @Test
    void buildJwtDecoderFromJwks_validRsaJwks_returnsDecoder() throws Exception {
        // buildJwtDecoderFromJwks is public for the introspect endpoint. Cover its happy path
        // so any regression that breaks the parse or decoder assembly fails here, not only
        // inside the introspect handler.
        String validRsaJwks = "{\"keys\":[{\"kty\":\"RSA\","
                + "\"n\":\"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFF"
                + "xuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6"
                + "Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n9"
                + "1CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-"
                + "csFCur-kEgU8awapJzKnqDKgw\",\"e\":\"AQAB\",\"alg\":\"RS256\",\"kid\":\"k1\"}]}";

        assertNotNull(resolver.buildJwtDecoderFromJwks(validRsaJwks),
                "Expected a usable JwtDecoder for a well-formed JWKS");
    }

    @Test
    void buildJwtDecoderFromJwks_malformedJwks_throws() {
        // Malformed JWKS should surface a ParseException so the introspect endpoint can
        // return an inactive response rather than silently succeeding.
        assertThrows(Exception.class,
                () -> resolver.buildJwtDecoderFromJwks("not-a-json-jwks"));
    }

    @Test
    void buildJwtDecoderFromJwks_decodesMatchingJwt() throws Exception {
        // End-to-end happy path: generate an RSA keypair, expose the public half as
        // JWKS, build a decoder, sign a JWT with the private key, and confirm the
        // decoder returns the expected claims. Guards against regressions that would
        // let the decoder return a usable-but-broken instance (e.g. wrong algorithm
        // family, missing validators) that only fails at introspect-time.
        RSAKey rsaJwk = generateRsaJwk("kid-under-test");
        String publicJwks = new JWKSet(rsaJwk.toPublicJWK()).toString();
        JwtDecoder decoder = resolver.buildJwtDecoderFromJwks(publicJwks);

        String signed = signJwt(rsaJwk, "kid-under-test",
                "https://test.issuer.example.com", "system:serviceaccount:nvca-system:nvca");

        Jwt decoded = decoder.decode(signed);
        assertEquals("https://test.issuer.example.com", decoded.getClaimAsString("iss"));
        assertEquals("system:serviceaccount:nvca-system:nvca", decoded.getClaimAsString("sub"));
    }

    @Test
    void buildJwtDecoderFromJwks_rejectsJwtSignedWithWrongKey() throws Exception {
        // Negative path: decoder is built from keypair-A's JWKS, token is signed with
        // keypair-B's private key. Decode must raise BadJwtException rather than
        // accepting the forged signature. Locks the security contract that
        // introspect's trust boundary is the stored JWKS, not arbitrary presented JWKs.
        RSAKey jwksA = generateRsaJwk("kid-a");
        RSAKey jwksB = generateRsaJwk("kid-b");
        // Decoder trusts keypair-A only.
        JwtDecoder decoder = resolver.buildJwtDecoderFromJwks(
                new JWKSet(jwksA.toPublicJWK()).toString());
        // But we sign with keypair-B, advertising keypair-A's kid to try to slip past
        // the key selector.
        String forged = signJwt(jwksB, "kid-a",
                "https://attacker.example.com", "system:serviceaccount:x:y");

        assertThrows(BadJwtException.class, () -> decoder.decode(forged));
    }

    private static RSAKey generateRsaJwk(String kid) throws Exception {
        KeyPairGenerator gen = KeyPairGenerator.getInstance("RSA");
        gen.initialize(2048);
        var kp = gen.generateKeyPair();
        return new RSAKey.Builder((RSAPublicKey) kp.getPublic())
                .privateKey((RSAPrivateKey) kp.getPrivate())
                .algorithm(JWSAlgorithm.RS256)
                .keyID(kid)
                .build();
    }

    private static String signJwt(RSAKey signingJwk, String kidHeader,
                                  String issuer, String subject) throws Exception {
        // Use an explicit kid in the header (may differ from signingJwk.keyID for the
        // "wrong key" negative test above — the decoder still routes by header kid but
        // the signature won't verify against the stored key under that kid).
        JWTClaimsSet claims = new JWTClaimsSet.Builder()
                .issuer(issuer)
                .subject(subject)
                .audience("nvcf-icms:cluster-test")
                .issueTime(new Date())
                .expirationTime(new Date(System.currentTimeMillis() + 60_000))
                .build();
        SignedJWT jwt = new SignedJWT(
                new JWSHeader.Builder(JWSAlgorithm.RS256).keyID(kidHeader).build(),
                claims);
        jwt.sign(new RSASSASigner(signingJwk));
        return jwt.serialize();
    }

    // --- Multi-issuer support (admin-issuer-uri) tests ---

    /** Build a Bearer header string whose unverified payload carries the given issuer. */
    private static String bearerWithIssuer(String issuer, String sub) {
        return bearerWithIssuerAndAudience(issuer, sub, "x");
    }

    private static String bearerWithIssuerAndAudience(String issuer, String sub, String audience) {
        String payload = String.format(
                "{\"iss\":\"%s\",\"sub\":\"%s\",\"aud\":[\"%s\"]}", issuer, sub, audience);
        String b64 = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        // Header alg=ES256 is mandatory for NimbusJwtDecoder's key selector, but we never
        // reach signature validation in these tests — resolution happens on unverified parse.
        return "Bearer eyJhbGciOiJFUzI1NiJ9." + b64 + ".sig";
    }

    @Test
    void authenticationManagerResolver_adminIssuerConfigured_routesAdminIssToStaticIssuerResolver() {
        // Given: resolver with admin-issuer set to api.nvcf (mirrors self-managed stack wiring).
        NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
        cfg.setOidcClusterIdentityEnabled(true);
        AuthManagerResolver multi = new AuthManagerResolver(
                apiKeysService, clusterRepository, cfg, "ES256",
                "http://api.sis.svc.cluster.local",
                "http://openbao/v1/services/sis-api/jwt/jwks",
                "http://api.nvcf.svc.cluster.local",
                "http://openbao/v1/services/nvcf-api/jwt/jwks",
                new TrustedJwtIssuerProperties(),
                /* apiKeyAuthEnabled */ true);
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                multi.authenticationManagerResolver();

        // When: request carries a token with iss=api.nvcf (admin-issuer-proxy-minted).
        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://api.nvcf.svc.cluster.local", "admin"));

        // Then: resolver takes the static issuer branch and returns a manager — not the cluster OIDC
        // branch (which would throw AuthenticationServiceException on missing nvcf-icms: audience).
        AuthenticationManager mgr = authResolver.resolve(req);
        assertNotNull(mgr, "iss=admin-issuer token must route to static issuer resolver");
        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
    }

    @Test
    void authenticationManagerResolver_adminIssuerBlank_unknownIssFallsThroughToClusterOidc() {
        // Given: default setUp() resolver with admin-issuer-uri blank.
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                resolver.authenticationManagerResolver();

        // When: token carries an unknown issuer (neither primary nor admin).
        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://random.example.com", "alice"));

        // Then: falls through to cluster OIDC, which throws because the aud=["x"] payload
        // lacks the nvcf-icms:{clusterId} marker. Single-issuer behavior is preserved.
        assertThrows(AuthenticationServiceException.class, () -> authResolver.resolve(req));
    }

    // --- Multi-issuer support (trusted-issuers[] list) tests ---

    @Test
    void authenticationManagerResolver_trustedIssuerConfigured_routesTrustedIssToStaticResolver() {
        // Given: resolver configured with a trusted-issuers[] entry (admin-issuer left blank).
        NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
        cfg.setOidcClusterIdentityEnabled(true);
        AuthManagerResolver multi = new AuthManagerResolver(
                apiKeysService, clusterRepository, cfg, "ES256",
                "http://api.sis.svc.cluster.local",
                "http://openbao/v1/services/sis-api/jwt/jwks",
                "",
                "",
                trustedIssuers(
                        "http://api.external-nvcf.example.com",
                        "http://openbao.external/v1/services/nvcf-api/jwt/jwks"),
                /* apiKeyAuthEnabled */ true);
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                multi.authenticationManagerResolver();

        // When: request carries a token whose iss matches the configured trusted issuer.
        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://api.external-nvcf.example.com", "admin"));

        // Then: resolver takes the static issuer branch and returns a manager — not the
        // cluster OIDC branch (which would throw on the missing nvcf-icms: audience).
        AuthenticationManager mgr = authResolver.resolve(req);
        assertNotNull(mgr, "iss=trusted-issuer token must route to static issuer resolver");
        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
    }

    @Test
    void authenticationManagerResolver_adminAndTrustedIssuersCoexist() {
        // Given: both the legacy admin-issuer-uri and an additional trusted-issuers[] entry.
        NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
        cfg.setOidcClusterIdentityEnabled(true);
        AuthManagerResolver multi = new AuthManagerResolver(
                apiKeysService, clusterRepository, cfg, "ES256",
                "http://api.sis.svc.cluster.local",
                "http://openbao/v1/services/sis-api/jwt/jwks",
                "http://api.nvcf.svc.cluster.local",
                "http://openbao/v1/services/nvcf-api/jwt/jwks",
                trustedIssuers(
                        "http://api.external-nvcf.example.com",
                        "http://openbao.external/v1/services/nvcf-api/jwt/jwks"),
                /* apiKeyAuthEnabled */ true);
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                multi.authenticationManagerResolver();

        // Then: the legacy admin issuer still routes to the static resolver...
        HttpServletRequest adminReq = mock(HttpServletRequest.class);
        when(adminReq.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://api.nvcf.svc.cluster.local", "admin"));
        assertNotNull(authResolver.resolve(adminReq),
                "legacy admin issuer must still route to static issuer resolver");

        // ...and so does the additional configured trusted issuer.
        HttpServletRequest trustedReq = mock(HttpServletRequest.class);
        when(trustedReq.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://api.external-nvcf.example.com", "admin"));
        assertNotNull(authResolver.resolve(trustedReq),
                "trusted-issuers[] entry must route to static issuer resolver");

        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
    }

    @Test
    void authenticationManagerResolver_trustedIssuerConfigured_unknownIssFallsThroughToClusterOidc() {
        // Given: a trusted-issuers[] entry is configured, but the token's iss matches neither
        // the primary, the (blank) admin, nor any trusted-issuers entry.
        NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
        cfg.setOidcClusterIdentityEnabled(true);
        AuthManagerResolver multi = new AuthManagerResolver(
                apiKeysService, clusterRepository, cfg, "ES256",
                "http://api.sis.svc.cluster.local",
                "http://openbao/v1/services/sis-api/jwt/jwks",
                "",
                "",
                trustedIssuers(
                        "http://api.external-nvcf.example.com",
                        "http://openbao.external/v1/services/nvcf-api/jwt/jwks"),
                /* apiKeyAuthEnabled */ true);
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                multi.authenticationManagerResolver();

        // When: token carries an issuer not present in the trusted set.
        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("http://random.example.com", "alice"));

        // Then: falls through to cluster OIDC, which throws because aud=["x"] lacks the
        // nvcf-icms:{clusterId} marker. Only listed issuers are trusted (fail-closed).
        assertThrows(AuthenticationServiceException.class, () -> authResolver.resolve(req));
    }

    @Test
    void authenticationManagerResolver_clusterOidcUnknownCluster_throwsGenericMessage() {
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                resolver.authenticationManagerResolver();

        String clusterId = "secret-cluster";
        when(clusterRepository.getClusterInfoByClusterId(clusterId, false))
                .thenReturn(java.util.Optional.empty());

        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuerAndAudience(
                        "http://random.example.com", "alice", "nvcf-icms:" + clusterId));

        AuthenticationServiceException ex =
                assertThrows(AuthenticationServiceException.class, () -> authResolver.resolve(req));
        assertEquals("Invalid bearer token", ex.getMessage());
        assertFalse(ex.getMessage().contains(clusterId));
    }

    @Test
    void authenticationManagerResolver_clusterOidcMalformedJwks_throwsGenericMessage() {
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                resolver.authenticationManagerResolver();

        String clusterId = "secret-cluster";
        ClusterEntity cluster = new ClusterEntity();
        cluster.setClusterId(clusterId);
        cluster.setJwks("not-a-json-jwks");
        when(clusterRepository.getClusterInfoByClusterId(clusterId, false))
                .thenReturn(java.util.Optional.of(cluster));

        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuerAndAudience(
                        "http://random.example.com", "alice", "nvcf-icms:" + clusterId));

        AuthenticationServiceException ex =
                assertThrows(AuthenticationServiceException.class, () -> authResolver.resolve(req));
        assertEquals("Invalid bearer token", ex.getMessage());
        assertFalse(ex.getMessage().contains(clusterId));
    }

    // --- api-key feature flag tests ---

    /** Build a resolver wired like setUp() but with the apiKeyAuthEnabled flag flipped. */
    private AuthManagerResolver buildResolverWithApiKeysAuth(boolean apiKeyAuthEnabled) {
        NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
        cfg.setOidcClusterIdentityEnabled(true);
        return new AuthManagerResolver(
                apiKeysService,
                clusterRepository,
                cfg,
                "ES256",
                "https://kubernetes.default.svc",
                "https://kubernetes.default.svc/openid/v1/jwks",
                "",
                "",
                new TrustedJwtIssuerProperties(),
                apiKeyAuthEnabled);
    }

    @Test
    void authenticationManagerResolver_apiKeyAuthEnabled_routesNvapiToApiKeyManager() {
        // Default managed-NVCF behavior: `Bearer nvapi-*` routes to the ApiKeys
        // OpaqueTokenAuthenticationProvider, which calls ApiKeysService → ApiKeys.
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                buildResolverWithApiKeysAuth(true).authenticationManagerResolver();

        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization")).thenReturn("Bearer nvapi-fakekey");

        AuthenticationManager mgr = authResolver.resolve(req);
        assertNotNull(mgr,
                "apiKeyAuthEnabled=true must route Bearer nvapi-* to a ApiKey specific AuthenticationManager");
    }

    @Test
    void authenticationManagerResolver_apiKeyAuthDisabled_returnsNullForNvapi() {
        // Self-hosted NVCF: ApiKeys disabled for ICMS. Resolver must return null so
        // BearerTokenAuthenticationFilter produces a clean 401, instead of
        // dialing ApiKeys service and crashing on missing creds.
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                buildResolverWithApiKeysAuth(false).authenticationManagerResolver();

        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization")).thenReturn("Bearer nvapi-fakekey");

        AuthenticationManager mgr = authResolver.resolve(req);
        assertNull(mgr,
                "apiKeyAuthEnabled=false must reject Bearer nvapi-* by returning no AuthenticationManager");
        // And critically, ApiKeysService must not be touched — that's the whole point.
        verifyNoInteractions(apiKeysService);
    }

    @Test
    void authenticationManagerResolver_apiKeyAuthDisabled_jwtIssuerStillRoutes() {
        // Flag must only gate the ApiKey branch. JWTs whose iss matches the
        // configured static issuer must still resolve via jwtResolver regardless
        // of apiKeyAuthEnabled — that's how self-hosted CLI register works
        // (Bearer eyJ... minted by admin-issuer-proxy).
        AuthenticationManagerResolver<HttpServletRequest> authResolver =
                buildResolverWithApiKeysAuth(false).authenticationManagerResolver();

        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn(bearerWithIssuer("https://kubernetes.default.svc", "system:serviceaccount:x:y"));

        AuthenticationManager mgr = authResolver.resolve(req);
        assertNotNull(mgr,
                "JWT with primary-issuer iss must resolve via jwtResolver even when apiKeyAuthEnabled=false");
    }

        /** Build a TrustedJwtIssuerProperties carrying one {issuer-uri, jwk-set-uri} entry. */
        private static TrustedJwtIssuerProperties trustedIssuers(String issuerUri, String jwkSetUri) {
            TrustedJwtIssuerProperties.TrustedIssuer entry =
                    new TrustedJwtIssuerProperties.TrustedIssuer();
            entry.setIssuerUri(issuerUri);
            entry.setJwkSetUri(jwkSetUri);
            TrustedJwtIssuerProperties props = new TrustedJwtIssuerProperties();
            props.setTrustedIssuers(List.of(entry));
            return props;
        }
}
