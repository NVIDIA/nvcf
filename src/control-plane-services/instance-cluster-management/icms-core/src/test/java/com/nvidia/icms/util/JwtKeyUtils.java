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
package com.nvidia.icms.util;

import static java.time.temporal.ChronoUnit.DAYS;
import static org.springframework.security.oauth2.core.oidc.IdTokenClaimNames.AZP;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jwt.JWTClaimsSet.Builder;
import com.nimbusds.jwt.SignedJWT;
import java.util.Date;
import java.util.List;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import org.bouncycastle.jce.provider.BouncyCastleProvider;


/**
 * This class is used to generate jwt with claims for mocks. You can use these
 * methods to acquire jwt tokens {@link JwtKeyUtils#getJwt}
 */
@UtilityClass
public class JwtKeyUtils {

    private static final String PRIVATE_MOCK_KEY =
            "{\"kty\":\"EC\",\"d\":\"HJEIhUwvq75M67Nn2SU1zEQJT4dlUdUFzGnWSZIMbmY\","
                    + "\"use\":\"sig\",\"crv\":\"P-256\",\"kid\":\"test-key-id\",\"x"
                    + "\":\"TUXxDKGvw3IZEHmFtR_7Lel2IuUdNed4xi3C520r4QE\",\"y\":\""
                    + "VsaPE5MCn0WrQ_UBLaaMCZhHYOZlvzuv1m4jTY6-9Hs\"}";

    private static final String TEST_KEY_ID = "test-key-id";
    private static final JWSAlgorithm jwsAlgorithm = JWSAlgorithm.parse("ES256");

    static {
        BouncyCastleProvider provider = new BouncyCastleProvider();
        java.security.Security.addProvider(provider);
    }

    public static String getAuthHeader(String sub, String... scopes) {
        return "Bearer " + getJwt(sub, scopes);
    }

    /**
     * Public JWKS (JSON) for the mock signing key used by all {@code getJwt} helpers. Served by the
     * test OAuth2 mock's {@code jwk-set-uri} so the resource server can verify tokens minted here.
     */
    @SneakyThrows
    public static String getPublicJwksJson() {
        JWK jwk = JWK.parse(PRIVATE_MOCK_KEY);
        return new JWKSet(jwk.toPublicJWK()).toString();
    }

    public static String getAuthHeaderWithAudience(
            String sub, List<String> audience, String... scopes) {
        return "Bearer " + getJwtWithAudience(sub, audience, scopes);
    }

    @SneakyThrows
    public static String getJwt(String sub, String... scopes) {
        JWK jwk = JWK.parse(PRIVATE_MOCK_KEY);
        return buildJwt(getTokenBuilder(List.of(scopes), sub), getSigner(jwk));
    }

    @SneakyThrows
    public static String getJwtWithAudience(
            String sub, List<String> audience, String... scopes) {
        JWK jwk = JWK.parse(PRIVATE_MOCK_KEY);
        Builder claimsSetBuilder = getTokenBuilder(List.of(scopes), sub);
        claimsSetBuilder.audience(audience);
        return buildJwt(claimsSetBuilder, getSigner(jwk));
    }

    @SneakyThrows(JOSEException.class)
    private static JWSSigner getSigner(JWK jwk) {
        return new ECDSASigner(jwk.toECKey());
    }

    @SneakyThrows(JOSEException.class)
    private static String buildJwt(Builder claimsSetBuilder, JWSSigner signer) {
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder(jwsAlgorithm)
                        .keyID(TEST_KEY_ID)
                        .build(),
                claimsSetBuilder.build());

        signedJWT.sign(signer);
        return signedJWT.serialize();
    }

    private static Builder getTokenBuilder(List<String> scopes, String sub) {
        Builder claimsSetBuilder = new Builder();
        claimsSetBuilder.audience(List.of("s:test-service", "test-client", "s:localhost"));
        claimsSetBuilder.subject(sub);
        setCommonClaims(claimsSetBuilder, scopes, "http://localhost:8082");
        return claimsSetBuilder;
    }

    private static void setCommonClaims(
            Builder claimsSetBuilder, List<String> scopes, String issuer) {
        Date now = new Date();
        claimsSetBuilder.issueTime(now);
        claimsSetBuilder.expirationTime(Date.from(now.toInstant().plus(36500, DAYS)));
        claimsSetBuilder.claim("scopes", scopes);
        claimsSetBuilder.claim(AZP, "test-client");
        claimsSetBuilder.issuer(issuer);
    }
}
