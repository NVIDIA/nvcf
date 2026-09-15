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

import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.configuration.PrivateJwksString;
import java.util.Base64;
import java.util.Map;
import lombok.Data;
import lombok.RequiredArgsConstructor;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
@RequiredArgsConstructor
public class JwtConfiguration {

    @Data
    @Configuration
    @ConfigurationProperties(prefix = "nvcf.jwks")
    public static class Settings {

        private String privateJwks;

        private Map<String, String> jweKeyMapping;
    }

    private final Settings settings;

    @Bean
    public PrivateJwksString privateJwks() {
        return new PrivateJwksString(
                new String(Base64.getDecoder().decode(settings.getPrivateJwks())));
    }

    @Bean
    public JweKeysMapping getJweKeysMapping() {
        return JweKeysMapping.builder()
                .keysMapping(settings.getJweKeyMapping())
                .build();
    }
}
