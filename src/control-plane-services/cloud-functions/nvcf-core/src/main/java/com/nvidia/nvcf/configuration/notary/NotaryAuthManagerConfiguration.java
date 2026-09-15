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
package com.nvidia.nvcf.configuration.notary;

import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_INVOKE_FUNCTION;
import static org.springframework.security.oauth2.core.OAuth2ErrorCodes.INVALID_REQUEST;
import static org.springframework.security.oauth2.core.OAuth2ErrorCodes.INVALID_TOKEN;

import com.nvidia.nvcf.configuration.IssuerAuthenticationManagerEntry;
import com.nvidia.nvcf.configuration.notary.NotaryConfiguration.NotaryConfigurationProperties;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.ComponentScan;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.convert.converter.Converter;
import org.springframework.security.authentication.AbstractAuthenticationToken;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.ProviderManager;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.OAuth2Error;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidatorResult;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationProvider;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@ComponentScan
@Configuration
@RequiredArgsConstructor
public class NotaryAuthManagerConfiguration {

    public static final Duration VALIDITY = Duration.ofMinutes(10);
    private static final Duration validityPlusSkew = VALIDITY.plusSeconds(10);

    private final NotaryConfigurationProperties notaryConfiguration;

    @Value("${nvcf.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;


    @Bean
    IssuerAuthenticationManagerEntry notaryAuthManager() {
        return new IssuerAuthenticationManagerEntry(notaryConfiguration.getIssuerUri(),
                                               notaryAuthenticationProvider());
    }

    private AuthenticationManager notaryAuthenticationProvider() {
        var jwtDecoder = NimbusJwtDecoder
                .withJwkSetUri(notaryConfiguration.getJwkSetUri())
                .jwsAlgorithm(notaryConfiguration.getJwsAlgorithms())
                .build();
        jwtDecoder.setJwtValidator(new CustomJwtValidator());
        var provider = new JwtAuthenticationProvider(jwtDecoder);
        provider.setJwtAuthenticationConverter(notaryJwtAuthConverter());
        return new ProviderManager(provider);
    }

    private static Converter<Jwt, AbstractAuthenticationToken> notaryJwtAuthConverter() {
        var converter = new NotaryServiceAuthenticationConverter();
        converter.setJwtGrantedAuthoritiesConverter(jwt -> {
            var customAuthority = new SimpleGrantedAuthority(SCOPE_INVOKE_FUNCTION);
            return List.of(customAuthority);
        });
        return converter;
    }

    private class CustomJwtValidator implements OAuth2TokenValidator<Jwt> {

        private static final JsonMapper JSON_MAPPER = new JsonMapper();

        @SneakyThrows
        @Override
        public OAuth2TokenValidatorResult validate(Jwt token) {
            var issuer = token.getIssuer();
            if (issuer == null || !issuer.toString().equals(notaryBaseUrl)) {
                log.debug("Invalid iss found in the token '{}'", token);
                return OAuth2TokenValidatorResult.failure(new OAuth2Error(INVALID_TOKEN));
            }
            var iat = token.getIssuedAt();
            if (iat == null) {
                log.debug("No iat found in the token '{}'", token);
                return OAuth2TokenValidatorResult.failure(new OAuth2Error(INVALID_TOKEN));
            }

            if (Duration.between(iat, Instant.now()).compareTo(validityPlusSkew) > 0) {
                log.debug("More than {} have passed since the token was issued", validityPlusSkew);
                return OAuth2TokenValidatorResult.failure(new OAuth2Error(INVALID_TOKEN));
            }
            // verify aud is correct
            var aud = token.getAudience().getFirst();
            if (aud == null || !aud.equals(nvcfAudience)) {
                log.debug("Notary Service token has invalid audience '{}'", aud);
                return OAuth2TokenValidatorResult.failure(new OAuth2Error(INVALID_TOKEN));
            }
            // verify function metadata matches
            var assertion = JSON_MAPPER.convertValue(token.getClaim("assertion"),
                                                     InvocationAssertion.class);
            if (assertion.clientId() == null || assertion.clientId().isBlank()) {
                log.debug("Notary Service token does not have a client id");
                return OAuth2TokenValidatorResult.failure(new OAuth2Error(INVALID_REQUEST));
            }

            return OAuth2TokenValidatorResult.success();
        }
    }
}
