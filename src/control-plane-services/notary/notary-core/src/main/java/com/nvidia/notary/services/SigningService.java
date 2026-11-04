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
package com.nvidia.notary.services;

import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSHeader.Builder;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.notary.config.NotaryProperties;
import com.nvidia.notary.vo.AssertionRequestVo;
import java.time.Clock;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@RequiredArgsConstructor
public class SigningService {

    public static final String CLAIM_ASSERTION = "assertion";

    private final JwtService jwtService;
    private final NotaryProperties notaryProperties;
    private final Clock clock;
    private final JtiGenerator jtiGenerator;

    /**
     * Builds JWT claims for signing using validated request parameters and signs the token.
     *
     * @param validatedRequest value object for validated request parameters.
     * @return signed JWT
     */
    public SignedJWT sign(AssertionRequestVo validatedRequest) {
        String callerClientId = validatedRequest.callerToken().getSubject();
        Instant now = clock.instant();
        String jti = jtiGenerator.generate();
        List<String> aud = validatedRequest.audienceServiceIds();
        String kid = notaryProperties.getSigningKid();

        JWTClaimsSet jwtClaimsSet = new JWTClaimsSet.Builder()
                .issuer(notaryProperties.getIssuerUrl())
                .jwtID(jti)
                .subject(callerClientId)
                .audience(aud)
                .claim(CLAIM_ASSERTION, validatedRequest.data())
                .issueTime(Date.from(now))
                .build();

        log.info(">>> signing using kid '{}' for client '{}' and aud '{}' with jti '{}' at '{}'",
                 kid, callerClientId, aud, jti, now);

        return getSignedToken(jwtClaimsSet, kid);
    }

    private SignedJWT getSignedToken(JWTClaimsSet jwtClaimsSet, String signingKid) {
        JWSHeader jwsHeader = new Builder(notaryProperties.getSigningAlgorithm())
                .keyID(signingKid)
                .build();

        SignedJWT signedJWT = new SignedJWT(jwsHeader, jwtClaimsSet);
        jwtService.signJwt(signedJWT, signingKid);
        return signedJWT;
    }

}
