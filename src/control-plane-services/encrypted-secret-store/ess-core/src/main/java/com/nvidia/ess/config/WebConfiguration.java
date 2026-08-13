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
package com.nvidia.ess.config;

import org.springframework.boot.convert.ApplicationConversionService;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;
import org.springframework.format.FormatterRegistry;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.web.reactive.config.WebFluxConfigurer;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import reactor.util.context.Context;

/**
 * Spring WebFlux does not route a request that has a trailing slash (e.g. {@code /a/b/}) to a
 * handler mapped without one ({@code /a/b}) — it returns 404. For background see
 * https://github.com/spring-projects/spring-framework/issues/28552 and
 * https://github.com/spring-projects/spring-framework/issues/31366.
 *
 * To keep {@code /foo/} resolving to a {@code /foo} mapping, a high-precedence {@link WebFilter}
 * (see {@link #trailingSlashNormalizationFilter()}) drops a single trailing slash before routing.
 * This is safe for the secret {@code /**} routes because secret paths are already
 * trailing-slash–normalized server-side by {@code SecretPathUtils} (so {@code .../foo} and
 * {@code .../foo/} already address the same secret).
 */
@Configuration
public class WebConfiguration implements WebFluxConfigurer {

    /**
     * Strips a single trailing {@code '/'} (never reducing the path below the root {@code "/"}) so
     * requests with a trailing slash still resolve to the slash-less handler mapping. Query string is
     * untouched (the path and query are separate components of the mutated request URI).
     */
    @Bean
    @Order(Ordered.HIGHEST_PRECEDENCE)
    WebFilter trailingSlashNormalizationFilter() {
        return (exchange, chain) -> {
            ServerHttpRequest request = exchange.getRequest();
            String rawPath = request.getURI().getRawPath();
            if (rawPath.length() > 1 && rawPath.endsWith("/")) {
                ServerHttpRequest mutated = request.mutate()
                        .path(rawPath.substring(0, rawPath.length() - 1))
                        .build();
                return chain.filter(exchange.mutate().request(mutated).build());
            }
            return chain.filter(exchange);
        };
    }

    // adds case insensitive ENUM converter
    @Override
    public void addFormatters(FormatterRegistry registry) {
        ApplicationConversionService.configure(registry);
    }

    /**
     * Puts {@link ServerWebExchange} into the Reactor context for all requests,
     * keyed by {@code ServerWebExchange.class} so that
     * {@link com.nvidia.ess.filter.ReactiveRequestContextHolder} can retrieve it.
     */
    @Bean
    WebFilter exchangeContextFilter() {
        return (exchange, chain) -> chain.filter(exchange)
                .contextWrite(Context.of(ServerWebExchange.class, exchange));
    }
}