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
package com.nvidia.icms.configuration.security;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.service.telemetry.RequestResponseTelemetryFilter;
import jakarta.servlet.http.HttpServletRequest;
import lombok.RequiredArgsConstructor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.config.annotation.method.configuration.EnableMethodSecurity;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.annotation.web.configuration.EnableWebSecurity;
import org.springframework.security.config.annotation.web.configuration.WebSecurityCustomizer;
import org.springframework.security.oauth2.server.resource.web.authentication.BearerTokenAuthenticationFilter;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.security.web.context.SecurityContextHolderFilter;

@Configuration
@EnableWebSecurity
@RequiredArgsConstructor
@EnableMethodSecurity
public class SecurityConfiguration {

    private final AuthEntryPoint authEntryPoint;
    private final RequestResponseTelemetryFilter requestResponseTelemetryFilter;
    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    @Bean
    public WebSecurityCustomizer webSecurityCustomizer() {
        return web -> {
            var ignoring = web.ignoring();
            ignoring.requestMatchers(
                    "/health",
                    "/v1/health",
                    "/v3/openapi",
                    "/actuator/**");
            if (nvcaConfigurationProperties.isOidcClusterIdentityEnabled()) {
                // NATS auth-callout webhook contract. Authentication is
                // delegated to the signed JWT in the request body (same
                // pipeline as /tokens/introspect), so Spring Security
                // must not short-circuit this request based on a missing
                // Authorization header — the auth-callout plugin
                // deliberately doesn't send one.
                ignoring.requestMatchers(
                        "/v1/nvca/tokens/introspect",
                        "/v1/si/oidc/tokens/introspect",
                        "/v1/si/oidc/token/introspect",
                        "/v1/nvca/nats-authorize",
                        "/v1/si/oidc/nats-authorize",
                        "/v1/si/oidc/natsAuthorize");
            }
        };
    }

    @Bean
    public SecurityFilterChain filterChain(
            HttpSecurity http,
            AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver)
            throws Exception {
        http
                .exceptionHandling(config -> config.authenticationEntryPoint(authEntryPoint))
                .csrf(csrf -> csrf.disable())
                .sessionManagement(session -> session.sessionCreationPolicy(
                        org.springframework.security.config.http.SessionCreationPolicy.STATELESS))
                .oauth2ResourceServer(configurer -> {
                    configurer.authenticationManagerResolver(authenticationManagerResolver);
                    configurer.authenticationEntryPoint(authEntryPoint);
                })
                .authorizeHttpRequests(auth -> auth.anyRequest().authenticated())
                .addFilterBefore(new JwtSizeLimitFilter(), BearerTokenAuthenticationFilter.class)
                .addFilterAfter(requestResponseTelemetryFilter,
                                SecurityContextHolderFilter.class);
        return http.build();
    }
}
