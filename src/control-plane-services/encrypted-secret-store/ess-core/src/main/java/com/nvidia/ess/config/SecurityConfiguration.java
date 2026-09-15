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

import com.nvidia.ess.constants.Constants;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication.Type;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.server.reactive.ServerHttpRequestDecorator;
import org.springframework.security.config.Customizer;
import org.springframework.security.config.annotation.method.configuration.EnableReactiveMethodSecurity;
import org.springframework.security.config.annotation.web.reactive.EnableWebFluxSecurity;
import org.springframework.security.config.web.server.ServerHttpSecurity;
import org.springframework.security.config.web.server.ServerHttpSecurity.CsrfSpec;
import org.springframework.security.config.web.server.ServerHttpSecurity.HeaderSpec.CacheSpec;
import org.springframework.security.config.web.server.ServerHttpSecurity.RequestCacheSpec;
import org.springframework.security.core.AuthenticationException;
import org.springframework.security.web.firewall.StrictHttpFirewall;
import org.springframework.security.web.server.SecurityWebFilterChain;
import org.springframework.security.web.server.ServerAuthenticationEntryPoint;
import org.springframework.security.web.server.util.matcher.NegatedServerWebExchangeMatcher;
import org.springframework.security.web.server.util.matcher.ServerWebExchangeMatchers;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import reactor.core.publisher.Mono;

@Configuration
@EnableWebFluxSecurity
@EnableReactiveMethodSecurity
public class SecurityConfiguration {

    @Bean("httpFirewall")
    public StrictHttpFirewall httpFirewall() {
        return new StrictHttpFirewall();
    }

    @Bean
    public SecurityWebFilterChain filterChain(ServerHttpSecurity http) {
        http.headers(h -> h.cache(CacheSpec::disable));

        http.exceptionHandling(c -> c.authenticationEntryPoint(new UnauthorizedAuthenticationEntryPoint()));

        http.cors(Customizer.withDefaults());
        // CSRF protection does not apply to ESS. Requests are authenticated by a
        // bearer JWT in the Authorization header (see AuthChecker), with no cookie
        // or session based authentication. Browsers do not auto-attach the
        // Authorization header to forged cross-site requests, so CSRF is not
        // exploitable here. Enabling it would only break non-browser service
        // clients without adding security.
        http.csrf(CsrfSpec::disable);

        http.requestCache(RequestCacheSpec::disable);
        http.securityMatcher(new NegatedServerWebExchangeMatcher(
                ServerWebExchangeMatchers.pathMatchers(Constants.getOpenEndpoints())));

        http.authorizeExchange(a -> a.anyExchange().authenticated());

        return http.build();
    }

    /**
     * Default UnAuthorizedEntryPoint handler. Customize this to return errors as you wish.
     */
    private static class UnauthorizedAuthenticationEntryPoint implements
            ServerAuthenticationEntryPoint {
        @Override
        public Mono<Void> commence(ServerWebExchange exchange, AuthenticationException ex) {
            exchange.getResponse().setStatusCode(HttpStatus.UNAUTHORIZED);
            return exchange.getResponse().writeWith(Mono.just(exchange.getResponse().bufferFactory()
                    .wrap(Constants.UNAUTHORIZED.getBytes())));
        }
    }

    // Fix for breaking changes in Spring Security https://github.com/spring-projects/spring-framework/issues/33789
    //  to allow modifying request headers
    // TODO Remove once the breaking change is fixed in Spring
    @Bean
    @Order(Ordered.HIGHEST_PRECEDENCE)
    @ConditionalOnWebApplication(type = Type.REACTIVE)
    WebFilter writeableHeaders() {
        return (exchange, chain) -> {
            HttpHeaders writeableHeaders = new HttpHeaders(exchange.getRequest().getHeaders());
            ServerHttpRequestDecorator writeableRequest = new ServerHttpRequestDecorator(
                    exchange.getRequest()) {
                @Override
                public HttpHeaders getHeaders() {
                    return writeableHeaders;
                }
            };
            ServerWebExchange writeableExchange = exchange.mutate()
                    .request(writeableRequest)
                    .build();
            return chain.filter(writeableExchange);
        };
    }
}

