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
package com.nvidia.nvcf.util;

import static net.minidev.json.parser.JSONParser.DEFAULT_PERMISSIVE_MODE;

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
import java.util.UUID;
import lombok.Getter;
import lombok.SneakyThrows;
import net.minidev.json.parser.JSONParser;

public class NotaryTokenUtils {

    private static final JWSSigner signer;

    @Getter
    private static final JWKSet jwks;

    static {
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

    @SneakyThrows
    public static String getJwt(String subject, String assertion, URL issuer, String audience, Date iat) {
        Date now = new Date();
        var parser = new JSONParser(DEFAULT_PERMISSIVE_MODE);
        var jsonAssertion = parser.parse(assertion);
        var claimsSetBuilder = new JWTClaimsSet.Builder()
                .subject(subject)
                .issueTime(now)
                .claim("assertion", jsonAssertion)
                .audience(audience)
                .issuer(issuer.toString())
                .issueTime(iat)
                .jwtID(UUID.randomUUID().toString());
        return getJwt(claimsSetBuilder);
    }

    @SneakyThrows(JOSEException.class)
    private static String getJwt(JWTClaimsSet.Builder claims) {
        var jwk = jwks.getKeys().get(0);
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder((JWSAlgorithm) jwk.getAlgorithm())
                        .keyID(jwk.getKeyID())
                        .build(),
                claims.build());
        signedJWT.sign(signer);
        return signedJWT.serialize();
    }

}