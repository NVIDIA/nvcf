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
package com.nvidia.notary.config;

import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import java.util.stream.Collectors;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.collections4.ListUtils;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Getter
@RefreshScope
@Service
public class PublicKeySet {

    private final String value;

    /**
     * Produces and caches the serialized value of public key set (JWKS).
     * Value will be refreshed every time jwkSet is refreshed.
     *
     * @param jwkSet current jwk set.
     */
    public PublicKeySet(JWKSet jwkSet) {
        JWKSet publicJWKSet = jwkSet.toPublicJWKSet();

        String publicKeyIds = ListUtils.emptyIfNull(publicJWKSet.getKeys())
                .stream()
                .map(JWK::getKeyID)
                .collect(Collectors.joining(","));
        log.info(">>> Using public keys set with key ids:  {}", publicKeyIds);

        this.value = publicJWKSet.toString();
    }
}
