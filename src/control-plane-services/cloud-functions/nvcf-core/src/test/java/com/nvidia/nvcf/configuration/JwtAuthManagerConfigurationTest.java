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
package com.nvidia.nvcf.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.gen.ECKeyGenerator;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.nvcf.configuration.TrustedJwtIssuerProperties.TrustedIssuer;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.service.apikeys.ApiKeysService;
import jakarta.servlet.http.HttpServletRequest;
import java.util.List;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpHeaders;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.server.resource.InvalidBearerTokenException;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;

// Closed JWKS ports: AuthenticationServiceException = routed, InvalidBearerTokenException = not.
class JwtAuthManagerConfigurationTest {

    private static final String PRIMARY_ISS = "http://api.nvcf.svc.cluster.local";
    private static final String PRIMARY_JWKS = "http://127.0.0.1:1/nvcf-api/jwks";
    private static final String EXTERNAL_ISS = "https://idp.example.com";
    private static final String EXTERNAL_JWKS = "http://127.0.0.1:1/idp/jwks";
    private static final String UNKNOWN_ISS = "https://attacker.example.com";

    private static ECKey signingKey;

    private final ApiKeysService apiKeysService = mock(ApiKeysService.class);
    private final JwtAuthManagerConfiguration jwtAuthManagerConfiguration =
            new JwtAuthManagerConfiguration(PRIMARY_ISS, PRIMARY_JWKS, "ES256");

    @BeforeAll
    static void generateSigningKey() throws Exception {
        signingKey = new ECKeyGenerator(Curve.P_256).keyID("test").generate();
    }

    @Test
    void registersThePrimaryIssuer() {
        var entry = jwtAuthManagerConfiguration.jwtAuthManager();

        assertThat(entry.issuer()).isEqualTo(PRIMARY_ISS);
        assertThat(entry.authenticationManager()).isNotNull();
    }

    @Test
    void routesTokensFromThePrimaryIssuerToAManager() {
        var resolver = resolver(properties());

        assertThatThrownBy(() -> authenticate(resolver, PRIMARY_ISS))
                .isInstanceOf(AuthenticationServiceException.class);
    }

    @Test
    void routesTokensFromEveryConfiguredTrustedIssuerToAManager() {
        var resolver = resolver(properties(trustedIssuer(EXTERNAL_ISS, EXTERNAL_JWKS)));

        assertThatThrownBy(() -> authenticate(resolver, EXTERNAL_ISS))
                .isInstanceOf(AuthenticationServiceException.class);
        assertThatThrownBy(() -> authenticate(resolver, PRIMARY_ISS))
                .isInstanceOf(AuthenticationServiceException.class);
    }

    @Test
    void rejectsTokensFromAnIssuerThatIsNotConfigured() {
        var resolver = resolver(properties(trustedIssuer(EXTERNAL_ISS, EXTERNAL_JWKS)));

        assertThatThrownBy(() -> authenticate(resolver, UNKNOWN_ISS))
                .isInstanceOf(InvalidBearerTokenException.class)
                .hasMessage("Invalid issuer");
    }

    @Test
    void skipsEntriesMissingIssuerOrJwkSetUri() {
        var resolver = resolver(properties(
                trustedIssuer(EXTERNAL_ISS, null),
                trustedIssuer("  ", EXTERNAL_JWKS)));

        assertThatThrownBy(() -> authenticate(resolver, EXTERNAL_ISS))
                .isInstanceOf(InvalidBearerTokenException.class)
                .hasMessage("Invalid issuer");
    }

    @Test
    void apiKeysBypassTheJwtResolver() {
        var apiKey = "nvapi-secret";
        when(apiKeysService.resolveNCAIdFromApiKey(apiKey)).thenReturn(new ApiKeyValidationResult(
                true, "nca-id", "nca-id", "owner-id",
                new ApiKeyValidationResult.Policy(List.of(), List.of("read"), "nvcf")));
        var resolver = resolver(properties());
        var request = new MockHttpServletRequest();
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + apiKey);

        Authentication authentication = resolver.resolve(request)
                .authenticate(new BearerTokenAuthenticationToken(apiKey));

        assertThat(authentication.getName()).isEqualTo("owner-id");
    }

    @Test
    void rejectsAnInvalidSignatureAlgorithm() {
        assertThatThrownBy(() ->
                new JwtAuthManagerConfiguration(PRIMARY_ISS, PRIMARY_JWKS, "NOT_AN_ALGORITHM"))
                .isInstanceOf(IllegalArgumentException.class);
    }

    private AuthenticationManagerResolver<HttpServletRequest> resolver(
            TrustedJwtIssuerProperties properties) {
        return new AuthManagerResolverConfiguration(
                List.of(jwtAuthManagerConfiguration.jwtAuthManager()),
                apiKeysService, jwtAuthManagerConfiguration, properties)
                .authenticationManagerResolver();
    }

    private static Authentication authenticate(
            AuthenticationManagerResolver<HttpServletRequest> resolver, String issuer) {
        var token = signedToken(issuer);
        var request = new MockHttpServletRequest();
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + token);
        return resolver.resolve(request).authenticate(new BearerTokenAuthenticationToken(token));
    }

    private static String signedToken(String issuer) {
        try {
            var jwt = new SignedJWT(
                    new JWSHeader.Builder(JWSAlgorithm.ES256).keyID(signingKey.getKeyID()).build(),
                    new JWTClaimsSet.Builder().issuer(issuer).subject("someone").build());
            jwt.sign(new ECDSASigner(signingKey));
            return jwt.serialize();
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    private static TrustedJwtIssuerProperties properties(TrustedIssuer... entries) {
        var properties = new TrustedJwtIssuerProperties();
        properties.setTrustedIssuers(List.of(entries));
        return properties;
    }

    private static TrustedIssuer trustedIssuer(String issuerUri, String jwkSetUri) {
        var entry = new TrustedIssuer();
        entry.setIssuerUri(issuerUri);
        entry.setJwkSetUri(jwkSetUri);
        return entry;
    }
}
