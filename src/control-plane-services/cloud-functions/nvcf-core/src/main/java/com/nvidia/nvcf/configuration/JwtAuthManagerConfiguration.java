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

import java.util.Collections;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtValidators;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationConverter;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationProvider;
import org.springframework.util.CollectionUtils;

@Configuration(proxyBeanMethods = false)
public class JwtAuthManagerConfiguration {

    private final String issuerUri;
    private final String jwkSetUri;
    private final SignatureAlgorithm jwsAlgorithm;

    public JwtAuthManagerConfiguration(
            @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}") String issuerUri,
            @Value("${spring.security.oauth2.resourceserver.jwt.jwk-set-uri}") String jwkSetUri,
            @Value("${spring.security.oauth2.resourceserver.jwt.jws-algorithms}") String jwsAlgorithm) {
        this.issuerUri = issuerUri;
        this.jwkSetUri = jwkSetUri;
        this.jwsAlgorithm = SignatureAlgorithm.valueOf(jwsAlgorithm); // Fail-fast if invalid.
    }

    @Bean
    IssuerAuthenticationManagerEntry jwtAuthManager() {
        return new IssuerAuthenticationManagerEntry(issuerUri, jwtAuthenticationManager());
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
