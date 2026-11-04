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

import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.configuration.PrivateJwksString;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * JWT wiring for notary. Provides the {@code PrivateJwksString} and {@code JweKeysMapping} beans
 * that nv-boot-starter-jwt's auto-configured {@code JwksConfiguration} consumes to register the
 * (refresh-scoped) {@code jwkSet} and {@code jwtService}.
 */
@Slf4j
@Configuration
@RequiredArgsConstructor
public class JwtConfiguration {

    private final NotaryProperties notaryProperties;

    @Bean
    @RefreshScope
    public PrivateJwksString privateJwks() {
        log.info("reloading private jwks");
        return new PrivateJwksString(notaryProperties.getPrivateJwks());
    }

    @Bean
    public JweKeysMapping getJweKeysMapping() {
        return JweKeysMapping.builder()
                // JwtService requires a non-empty mapping even when notary never uses JWE.
                .keysMapping(Map.of("dummy-jwe-key-name", "value"))
                .build();
    }

}
