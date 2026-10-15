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

import static org.springframework.security.oauth2.core.OAuth2AccessToken.TokenType.BEARER;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.EXP;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.IAT;

import com.nvidia.nvcf.service.apikeys.ApiKeysService;
import jakarta.servlet.http.HttpServletRequest;
import java.time.Instant;
import java.util.List;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.oauth2.core.OAuth2AccessToken;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthentication;
import org.springframework.security.oauth2.server.resource.authentication.JwtIssuerAuthenticationManagerResolver;
import org.springframework.security.oauth2.server.resource.authentication.OpaqueTokenAuthenticationProvider;
import org.springframework.security.oauth2.server.resource.introspection.OpaqueTokenAuthenticationConverter;
import org.springframework.security.oauth2.server.resource.introspection.OpaqueTokenIntrospector;

@RequiredArgsConstructor
@Configuration(proxyBeanMethods = false)
public class AuthManagerResolverConfiguration {

    private final List<IssuerAuthenticationManagerEntry> jwtAuthManagers;
    private final ApiKeysService apiKeysService;

    @Bean
    AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver() {
        var jwtResolver = jwtResolver();
        return request -> {
            // Use the ApiKey AuthenticationManager if the Bearer token starts with nvapi.
            var authorization = request.getHeader(HttpHeaders.AUTHORIZATION);
            if (authorization != null && authorization.startsWith("Bearer nvapi-")) {
                return apiKeyAuthenticationManager();
            }
            // Defaults to resolve to AuthenticationManager based on iss claim in the token.
            return jwtResolver.resolve(request);
        };
    }

    private AuthenticationManager apiKeyAuthenticationManager() {
        var provider = new OpaqueTokenAuthenticationProvider(apiKeyIntrospector());
        provider.setAuthenticationConverter(apiKeyConverter());
        return provider::authenticate;
    }

    private OpaqueTokenIntrospector apiKeyIntrospector() {
        return token -> apiKeysService.resolveNCAIdFromApiKey(token).getOAuth2Principal();
    }

    private static OpaqueTokenAuthenticationConverter apiKeyConverter() {
        return (introspectedToken, authenticatedPrincipal) -> {
            Instant iat = authenticatedPrincipal.getAttribute(IAT);
            Instant exp = authenticatedPrincipal.getAttribute(EXP);
            var accessToken = new OAuth2AccessToken(BEARER, introspectedToken, iat, exp);
            return new BearerTokenAuthentication(authenticatedPrincipal, accessToken,
                                                 authenticatedPrincipal.getAuthorities());
        };
    }

    private JwtIssuerAuthenticationManagerResolver jwtResolver() {
        var managers = jwtAuthManagers
                .stream()
                .collect(Collectors.toMap(
                        IssuerAuthenticationManagerEntry::issuer,
                        IssuerAuthenticationManagerEntry::authenticationManager));
        return new JwtIssuerAuthenticationManagerResolver(managers::get);
    }
}
