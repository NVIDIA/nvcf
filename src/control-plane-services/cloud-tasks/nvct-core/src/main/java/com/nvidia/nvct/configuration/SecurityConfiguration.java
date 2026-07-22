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
package com.nvidia.nvct.configuration;

import static org.springframework.security.config.http.SessionCreationPolicy.STATELESS;

import com.nvidia.nvct.configuration.filters.ExceptionHandlerFilter;
import jakarta.servlet.http.HttpServletRequest;
import lombok.RequiredArgsConstructor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.annotation.web.configuration.EnableWebSecurity;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.security.web.authentication.logout.LogoutFilter;

@Configuration
@RequiredArgsConstructor
@EnableWebSecurity
public class SecurityConfiguration {

    @Bean
    public SecurityFilterChain filterChain(
            HttpSecurity http,
            AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver,
            ExceptionHandlerFilter exceptionHandlerFilter) {
        http
                .csrf(csrf -> csrf.disable())
                .sessionManagement(session -> session.sessionCreationPolicy(STATELESS))
                // Reuse the ValidationAwareExceptionHandler to handle the exception inside
                // the filters,especially for the  custom authentication resolver exceptions.
                .addFilterBefore(exceptionHandlerFilter, LogoutFilter.class)
                // Enable JWT and api-key security
                .oauth2ResourceServer(configurer -> configurer
                                    .authenticationManagerResolver(authenticationManagerResolver))
                .authorizeHttpRequests(
                        request -> request
                                .requestMatchers("/health").permitAll()
                                .requestMatchers("/v3/openapi").permitAll()
                                // management port is not exposed via load balancer. Readiness
                                // and liveness probes, metrics, prometheus etc. are accessible
                                // via management port.
                                .requestMatchers("/actuator/**").permitAll()
                                .anyRequest().authenticated());
        return http.build();
    }
}
