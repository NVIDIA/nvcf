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

package com.nvidia.boot.mock.oauth2;

import static java.time.temporal.ChronoUnit.SECONDS;
import static org.springframework.security.oauth2.core.oidc.IdTokenClaimNames.AZP;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import java.net.URL;
import java.security.KeyPairGenerator;
import java.security.interfaces.ECPrivateKey;
import java.security.interfaces.ECPublicKey;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import org.apache.commons.lang3.StringUtils;
import org.jspecify.annotations.Nullable;

/**
 * Test utilities for JWT generation using Mock OAuth2 Token Server.
 */
@UtilityClass
public class OAuth2TestUtils {

    private static final JWSSigner signer;

    @Getter
    private static final JWKSet jwks;

    static {
        try {
            var gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(Curve.P_256.toECParameterSpec());
            var keyPair = gen.generateKeyPair();

            var privateJwk = new ECKey.Builder(Curve.P_256,
                                               (ECPublicKey) keyPair.getPublic())
                    .privateKey((ECPrivateKey) keyPair.getPrivate())
                    .keyID(UUID.randomUUID().toString())
                    .algorithm(JWSAlgorithm.ES256)
                    .build();
            var publicJWK = privateJwk.toPublicJWK();
            jwks = new JWKSet(publicJWK);
            signer = new ECDSASigner(privateJwk);
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    @SneakyThrows(JOSEException.class)
    public static String getJwt(JWTClaimsSet.Builder claims) {
        var jwk = jwks.getKeys().get(0);
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder((JWSAlgorithm) jwk.getAlgorithm())
                        .keyID(jwk.getKeyID())
                        .build(),
                claims.build());
        signedJWT.sign(signer);
        return signedJWT.serialize();
    }

    public static String getJwt(
            String subject, List<String> scopes, int expireInSeconds, URL issuer, String options) {
        return getJwt(subject, scopes, expireInSeconds, issuer, options, null);
    }

    public static String getJwt(
            String subject, List<String> scopes, int expireInSeconds, URL issuer, String options,
            @Nullable Map<String, String> metadata) {
        if (scopes == null) {
            scopes = List.of();
        }

        Date now = new Date();
        var claimsSetBuilder = new JWTClaimsSet.Builder()
                .subject(subject)
                .issueTime(now)
                .expirationTime(Date.from(now.toInstant().plus(expireInSeconds, SECONDS)))
                .claim("scopes", scopes)
                .audience(List.of(getServiceId(issuer), subject))
                .issuer(issuer.toString())
                .claim(AZP, subject);

        if (StringUtils.isNotBlank(options)) {
            claimsSetBuilder.claim("options", options);
        }

        if (metadata != null && !metadata.isEmpty()) {
            claimsSetBuilder.claim("metadata", metadata);
        }

        return getJwt(claimsSetBuilder);
    }

    public static String getJwt(
            String subject, List<String> scopes, int expireInSeconds, URL issuer) {
        return getJwt(subject, scopes, expireInSeconds, issuer, null);
    }

    @SneakyThrows
    public static String getJwt(URL issuer, String... scopes) {
        return getJwt("test-client", List.of(scopes), 100, issuer);
    }

    /**
     * Extracts a service ID from an issuer URL.
     * The service ID is "s:" followed by the first segment of the hostname.
     */
    public static String getServiceId(URL issuer) {
        var host = issuer.getHost();
        var index = host.indexOf('.');
        var endIndex = (index != -1) ? index : host.length();
        return "s:" + host.substring(0, endIndex);
    }
}
