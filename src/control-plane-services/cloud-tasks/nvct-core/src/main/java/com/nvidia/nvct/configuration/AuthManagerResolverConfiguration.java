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

import static org.springframework.security.oauth2.core.OAuth2AccessToken.TokenType.BEARER;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.EXP;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.IAT;

import com.nvidia.nvct.service.apikeys.ApiKeysService;
import jakarta.servlet.http.HttpServletRequest;
import java.time.Instant;
import java.util.Collections;
import java.util.Map;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.OAuth2AccessToken;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtValidators;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthentication;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationConverter;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationProvider;
import org.springframework.security.oauth2.server.resource.authentication.JwtIssuerAuthenticationManagerResolver;
import org.springframework.security.oauth2.server.resource.authentication.OpaqueTokenAuthenticationProvider;
import org.springframework.security.oauth2.server.resource.introspection.OpaqueTokenAuthenticationConverter;
import org.springframework.security.oauth2.server.resource.introspection.OpaqueTokenIntrospector;
import org.springframework.util.CollectionUtils;

@Configuration(proxyBeanMethods = false)
public class AuthManagerResolverConfiguration {

    private final ApiKeysService apiKeysService;
    private final String issuerUri;
    private final String jwkSetUri;
    private final SignatureAlgorithm jwsAlgorithm;


    public AuthManagerResolverConfiguration(
            ApiKeysService apiKeysService,
            @Value("${spring.security.oauth2.resourceserver.jwt.jws-algorithms}") String jwsAlgorithm,
            @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}") String issuerUri,
            @Value("${spring.security.oauth2.resourceserver.jwt.jwk-set-uri}") String jwkSetUri) {
        this.apiKeysService = apiKeysService;
        this.issuerUri = issuerUri;
        this.jwkSetUri = jwkSetUri;
        this.jwsAlgorithm = SignatureAlgorithm.valueOf(jwsAlgorithm); // Fail-fast if invalid.
    }

    @Bean
    AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver() {
        var jwtResolver = jwtResolver();
        return request -> {
            var authorization = request.getHeader(HttpHeaders.AUTHORIZATION);
            if (authorization != null && authorization.startsWith("Bearer nvapi-")) {
                return apiKeyAuthenticationManager();
            }
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
        var managers = Map.of(issuerUri, jwtAuthenticationManager());
        return new JwtIssuerAuthenticationManagerResolver(managers::get);
    }

    private AuthenticationManager jwtAuthenticationManager() {
        var provider = new JwtAuthenticationProvider(jwtDecoder());
        provider.setJwtAuthenticationConverter(jwtAuthenticationConverter());
        return provider::authenticate;
    }

    private JwtDecoder jwtDecoder() {
        var decoder = NimbusJwtDecoder
                .withJwkSetUri(jwkSetUri)
                .jwsAlgorithm(jwsAlgorithm)
                .build();
        decoder.setJwtValidator(JwtValidators.createDefaultWithIssuer(issuerUri));
        return decoder;
    }

    private JwtAuthenticationConverter jwtAuthenticationConverter() {
        var converter = new JwtAuthenticationConverter();
        converter.setJwtGrantedAuthoritiesConverter(jwt -> {
            var scopes = jwt.getClaimAsStringList("scopes");
            if (CollectionUtils.isEmpty(scopes)) {
                return Collections.emptyList();
            }
            return scopes.stream()
                    .map(SimpleGrantedAuthority::new)
                    .map(GrantedAuthority.class::cast)
                    .toList();
        });
        return converter;
    }
}
