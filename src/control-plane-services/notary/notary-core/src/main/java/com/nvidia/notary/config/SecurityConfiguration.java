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
package com.nvidia.notary.config;

import static org.springframework.security.config.http.SessionCreationPolicy.STATELESS;

import jakarta.annotation.PostConstruct;
import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.stream.Collectors;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.restclient.RestTemplateBuilder;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.config.annotation.method.configuration.EnableMethodSecurity;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.annotation.web.configuration.EnableWebSecurity;
import org.springframework.security.config.annotation.web.configurers.AbstractHttpConfigurer;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DelegatingOAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtClaimValidator;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtValidators;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationConverter;
import org.springframework.security.web.SecurityFilterChain;


@Configuration
@EnableWebSecurity
@EnableMethodSecurity
public class SecurityConfiguration {

    /**
     * Incoming access tokens carry scopes in the {@code scopes} claim as a JSON array, not the
     * standard OAuth 2 {@code scope} / {@code scp} space-delimited string — so Spring's default
     * authorities converter does not work and we set this claim name explicitly.
     */
    private static final String SCOPES_CLAIM = "scopes";

    @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}")
    private String issuerUri;

    @Value("${spring.security.oauth2.resourceserver.jwt.jwk-set-uri}")
    private String jwkSetUri;

    @Value("${spring.security.oauth2.resourceserver.jwt.jws-algorithms}")
    private List<String> jwsAlgorithms;

    /**
     * Fail fast at bean-init time when {@code issuer-uri} is blank. {@link NimbusJwtDecoder}
     * accepts an empty/relative {@code jwk-set-uri} at construction and only blows up at the
     * first JWT validation, so without this guard a misconfigured pod would pass k8s readiness
     * probes and return 401 silently on every {@code /sign} call. Throwing here aborts the bean
     * factory, which in turn fails the readiness probe and blocks the rollout.
     */
    @PostConstruct
    void validateIssuerUri() {
        if (StringUtils.isBlank(issuerUri)) {
            throw new IllegalStateException(
                    "spring.security.oauth2.resourceserver.jwt.issuer-uri must be set; "
                    + "configure AUTH_TOKEN_ISSUER (or your profile's issuer-uri override). "
                    + "Without it Spring cannot validate caller JWTs and /sign would return 401 "
                    + "for every request, including those from valid issuers.");
        }
    }

    private final NotaryProperties notaryProperties;

    public SecurityConfiguration(NotaryProperties notaryProperties) {
        this.notaryProperties = notaryProperties;
    }

    @Bean
    public SecurityFilterChain filterChain(HttpSecurity http) throws Exception {
        return http
                // Disable CSRF since this service can be hit from anywhere
                .csrf(AbstractHttpConfigurer::disable) //NOSONAR
                .sessionManagement(session -> session.sessionCreationPolicy(STATELESS))
                .authorizeHttpRequests(
                        request -> request
                                .requestMatchers("/.well-known/jwks.json").permitAll()
                                .requestMatchers("/health").permitAll()
                                // everything under admin is not exposed via load balancer and only
                                // accessible via admin port
                                // it provides health readiness and liveness probes and metrics
                                .requestMatchers("/actuator/**").permitAll()
                                .anyRequest().authenticated())
                .oauth2ResourceServer(oauth2 -> oauth2
                        .jwt(jwt -> jwt.jwtAuthenticationConverter(jwtAuthenticationConverter())))
                .build();
    }

    /**
     * Emits authorities raw (no {@code SCOPE_} prefix) to match the expression
     * {@code hasAuthority(@notaryProperties.signingScope)} used by {@code NotaryController}.
     */
    @Bean
    public JwtAuthenticationConverter jwtAuthenticationConverter() {
        JwtAuthenticationConverter converter = new JwtAuthenticationConverter();
        converter.setJwtGrantedAuthoritiesConverter(jwt -> {
            List<String> scopes = jwt.getClaimAsStringList(SCOPES_CLAIM);
            if (scopes == null) {
                return List.of();
            }
            return scopes.stream()
                    .map(SimpleGrantedAuthority::new)
                    .collect(Collectors.<GrantedAuthority>toList());
        });
        return converter;
    }

    /**
     * Builds a {@link JwtDecoder} that accepts the JWS algorithms listed in
     * {@code spring.security.oauth2.resourceserver.jwt.jws-algorithms} and composes issuer +
     * optional audience validation. When {@link NotaryProperties#getRequiredAudiences()} is empty,
     * no audience check runs (used by the NCP profile, where the issuer check alone is
     * sufficient).
     *
     * <p>The injected {@link RestTemplateBuilder} carries Spring Boot's
     * {@code MetricsRestTemplateCustomizer}, so JWKS fetches are recorded as
     * {@code http.client.requests} on the configured {@link io.micrometer.core.instrument.MeterRegistry}.
     * Without this, {@link NimbusJwtDecoder} would build its own non-instrumented {@code RestTemplate}
     * and the outbound JWKS call would never appear in {@code /actuator/prometheus}.
     */
    @Bean
    public JwtDecoder jwtDecoder(RestTemplateBuilder restTemplateBuilder) {
        NimbusJwtDecoder decoder = NimbusJwtDecoder.withJwkSetUri(jwkSetUri)
                .restOperations(restTemplateBuilder.build())
                .jwsAlgorithms(algs -> jwsAlgorithms.stream()
                        .map(SignatureAlgorithm::from)
                        .forEach(algs::add))
                .build();

        List<OAuth2TokenValidator<Jwt>> validators = new ArrayList<>();
        validators.add(JwtValidators.createDefaultWithIssuer(issuerUri));
        List<String> requiredAudiences = notaryProperties.getRequiredAudiences();
        if (!requiredAudiences.isEmpty()) {
            validators.add(new JwtClaimValidator<Collection<String>>(
                    "aud",
                    aud -> aud != null && requiredAudiences.stream().anyMatch(aud::contains)));
        }
        decoder.setJwtValidator(new DelegatingOAuth2TokenValidator<>(validators));
        return decoder;
    }

}
