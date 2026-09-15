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

package com.nvidia.boot.telemetry.client;

import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import java.time.Duration;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.validation.annotation.Validated;

/**
 * Configuration properties for telemetry. Apps must add this bean to the Spring context.
 * Load OAuth2 config from {@code spring.security.oauth2.client.registration.telemetry} and
 * {@code spring.security.oauth2.client.provider.telemetry.token-uri}, plus Telemetry Server
 * url, source, pathPrefix, and timeouts.
 */
@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
@Validated
public class TelemetryProperties {

    @NotBlank(message = "Telemetry url is required")
    private String url;

    /**
     * Path prefix for the endpoint (e.g. {@code /api/v2/topic}). A trailing {@code /} is
     * optional; {@link TelemetryClient} strips it before appending {@code /{resourceName}}.
     */
    @NotBlank(message = "Telemetry pathPrefix is required (e.g., /api/v2/topic)")
    private String pathPrefix;

    @NotBlank(message = "Telemetry source is required")
    private String source;

    @Valid
    @Builder.Default
    private OAuth2Properties oauth2 = new OAuth2Properties();

    @Builder.Default
    private Duration connectTimeout = Duration.ofSeconds(30);

    @Builder.Default
    private Duration readTimeout = Duration.ofSeconds(30);

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class OAuth2Properties {

        @NotBlank(message = "OAuth2 token-uri is required for telemetry")
        private String tokenUri;

        @NotBlank(message = "OAuth2 client-id is required for telemetry")
        private String clientId;

        @NotBlank(message = "OAuth2 client-secret is required for telemetry")
        private String clientSecret;

        private String scope;

        @Builder.Default
        private OAuth2AuthMethod authMethod = OAuth2AuthMethod.CLIENT_SECRET_POST;
    }
}
