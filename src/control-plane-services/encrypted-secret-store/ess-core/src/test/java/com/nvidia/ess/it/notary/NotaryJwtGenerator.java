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
package com.nvidia.ess.it.notary;

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
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.Getter;
import lombok.SneakyThrows;

public class NotaryJwtGenerator {

    private final JWSSigner signer;

    @Getter
    private final JWKSet jwks;

    public NotaryJwtGenerator() {
        try {
            var gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(Curve.P_256.toECParameterSpec());
            var keyPair = gen.generateKeyPair();

            // Convert to JWK format
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
    public String getJwt(
            String subject, URL issuer, List<String> audiences, Map<String, Object> data) {
        return getJwt(signer, jwks.toPublicJWKSet(), subject, issuer, audiences, data);
    }

    @SneakyThrows(JOSEException.class)
    public static String getJwt(JWSSigner signer, JWKSet jwks, JWTClaimsSet.Builder claims) {
        var jwk = jwks.getKeys().get(0);
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder(new JWSAlgorithm(jwk.getAlgorithm().getName()))
                        .keyID(jwk.getKeyID())
                        .build(),
                claims.build());
        signedJWT.sign(signer);
        return signedJWT.serialize();
    }

    public static String getJwt(JWSSigner signer, JWKSet jwks, String subject, URL issuer, List<String> audiences, Map<String, Object> data) {
        Instant now = Instant.now();

        var claimsSetBuilder = new JWTClaimsSet.Builder()
                .issuer(issuer.toString())
                .jwtID(UUID.randomUUID().toString())
                .subject(subject)
                .audience(audiences)
                .claim("assertion", data)
                .issueTime(Date.from(now));


        return getJwt(signer, jwks, claimsSetBuilder);
    }
}
