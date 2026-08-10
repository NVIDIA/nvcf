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

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.auth.jwk.JwkSetService;
import org.apache.commons.lang3.exception.ExceptionUtils;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.NimbusReactiveJwtDecoder;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Service
public class JwtSignatureValidationService {
    private final JwkSetService jwkSetService;
    public JwtSignatureValidationService(JwkSetService jwkSetService) {
        this.jwkSetService = jwkSetService;
    }

    public Mono<Jwt> getJwt(String jwksUrl, JWSAlgorithm algorithm, String tokenString) {

        return NimbusReactiveJwtDecoder
                .withJwkSource(jwkSource -> jwkSetService.getJwkSet(jwksUrl).flatMapIterable(JWKSet::getKeys))
                .jwsAlgorithm(algorithm::getName)
                .build()
                .decode(tokenString)
                .onErrorResume(e -> {
                    if (e instanceof UnauthorizedException) {
                        return Mono.error(e);
                    }
                    return Mono.error(new UnauthorizedException(String.format("JWT validation error: %s", ExceptionUtils.getRootCause(e).getMessage())));
                });
    }
}