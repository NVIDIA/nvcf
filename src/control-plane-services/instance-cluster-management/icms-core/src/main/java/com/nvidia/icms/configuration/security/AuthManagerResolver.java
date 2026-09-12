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

import static org.springframework.security.oauth2.core.OAuth2AccessToken.TokenType.BEARER;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.EXP;
import static org.springframework.security.oauth2.core.OAuth2TokenIntrospectionClaimNames.IAT;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.source.ImmutableJWKSet;
import com.nimbusds.jose.proc.JWSVerificationKeySelector;
import com.nimbusds.jose.proc.SecurityContext;
import com.nimbusds.jwt.JWTParser;
import com.nimbusds.jwt.proc.DefaultJWTProcessor;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.apikeys.ApiKeysService;
import com.nvidia.icms.util.AuthUtils;
import jakarta.servlet.http.HttpServletRequest;
import java.text.ParseException;
import java.time.Instant;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;
import org.springframework.security.authentication.ProviderManager;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DelegatingOAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2AccessToken;
import org.springframework.security.oauth2.core.OAuth2Error;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidatorResult;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtTimestampValidator;
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

@Slf4j
@Configuration(proxyBeanMethods = false)
public class AuthManagerResolver {

    private static final String INVALID_BEARER_TOKEN = "Invalid bearer token";

    /** Audience prefix for cluster-specific PSAT tokens (format: nvcf-icms:{clusterId}). */
    static final String AUDIENCE_PREFIX = "nvcf-icms:";

    /** Supported JWT signing algorithms for cluster OIDC tokens. */
    private static final Set<JWSAlgorithm> SUPPORTED_JWS_ALGORITHMS = Set.of(
            JWSAlgorithm.RS256, JWSAlgorithm.RS384, JWSAlgorithm.RS512,
            JWSAlgorithm.ES256, JWSAlgorithm.ES384, JWSAlgorithm.ES512
    );

    private final ApiKeysService apiKeysService;
    private final ClusterRepository clusterRepository;
    private final NvcaConfigurationProperties nvcaConfig;
    /**
     * Trusted static JWT issuers, mapping each accepted {@code iss} claim to the JWKS URI
     * used to verify its signatures. Built once at construction from the primary issuer,
     * the legacy single admin issuer, and any configured {@code trusted-issuers[]} entries.
     * Insertion order is preserved for deterministic manager registration; the primary
     * issuer is always first.
     */
    private final Map<String, String> trustedIssuerJwkSetUris;
    private final SignatureAlgorithm jwsAlgorithm;
    private final boolean apiKeyAuthEnabled;

    public AuthManagerResolver(
            ApiKeysService apiKeysService,
            ClusterRepository clusterRepository,
            NvcaConfigurationProperties nvcaConfig,
            @Value("${spring.security.oauth2.resourceserver.jwt.jws-algorithms:ES256}")
            String jwsAlgorithm,
            @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}") String issuerUri,
            @Value("${spring.security.oauth2.resourceserver.jwt.jwk-set-uri}") String jwkSetUri,
            // Optional legacy single admin issuer. When set, tokens whose iss matches this
            // admin issuer are also accepted (e.g. api.nvcf for CLI admin tokens minted by
            // admin-issuer-proxy via the nvcf-api JWT mount — scope cluster-management).
            // Retained for backward compatibility; it is folded into the trusted-issuer set
            // as one entry alongside icms.security.jwt.trusted-issuers[]. Blank default preserves
            // single-issuer behavior for existing deployments.
            @Value("${spring.security.oauth2.resourceserver.jwt.admin-issuer-uri:}")
            String adminIssuerUri,
            @Value("${spring.security.oauth2.resourceserver.jwt.admin-jwk-set-uri:}")
            String adminJwkSetUri,
            // Zero or more additional trusted static issuers (trusted-issuers[]), so tokens
            // from multiple external issuers can be trusted, keyed by iss. Empty by default,
            // preserving single/dual-issuer behavior.
            TrustedJwtIssuerProperties trustedIssuerProperties,
            @Value("${icms.nvca.api-key.enabled:true}") boolean apiKeyAuthEnabled) {
        this.apiKeysService = apiKeysService;
        this.clusterRepository = clusterRepository;
        this.nvcaConfig = nvcaConfig;
        this.trustedIssuerJwkSetUris = buildTrustedIssuerJwkSetUris(
                issuerUri, jwkSetUri, adminIssuerUri, adminJwkSetUri, trustedIssuerProperties);
        this.jwsAlgorithm = SignatureAlgorithm.valueOf(jwsAlgorithm);
        this.apiKeyAuthEnabled = apiKeyAuthEnabled;
    }

    @Bean
    AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver() {
        var jwtResolver = jwtResolver();
        var authenticationManager = apiKeyAuthenticationManager();
        return request -> {
            var authorization = request.getHeader(HttpHeaders.AUTHORIZATION);
            if (hasBearerPrefix(authorization)
                    && authorization.substring("Bearer ".length()).startsWith("nvapi-")) {
                if (!apiKeyAuthEnabled) {
                    // Self-hosted NVCF: api-keys are not enabled.
                    return null;
                }
                return authenticationManager;
            }

            // Known static issuer (primary, admin-issuer-proxy, or a configured trusted
            // external issuer) — use native Spring resolver.
            String issuer = extractIssuerFromToken(authorization);
            if (isTrustedStaticIssuer(issuer)) {
                return jwtResolver.resolve(request);
            }

            // Unknown issuer — try cluster OIDC (PSAT) resolution.
            // Guarded by oidcClusterIdentityEnabled flag so managed NVCF deployments (flag off)
            // reject unknown-issuer tokens via the static issuer resolver's own "no manager" path.
            if (!nvcaConfig.isOidcClusterIdentityEnabled()) {
                return jwtResolver.resolve(request);
            }
            return resolveClusterOidcIssuer(request);
        };
    }

    private AuthenticationManager apiKeyAuthenticationManager() {
        var provider = new OpaqueTokenAuthenticationProvider(apiKeyIntrospector());
        provider.setAuthenticationConverter(apiKeyConverter());
        return provider::authenticate;
    }

    private OpaqueTokenIntrospector apiKeyIntrospector() {
        return token -> apiKeysService.fetchValidationResult(token).getOAuth2Principal();
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
        Map<String, AuthenticationManager> managers = new HashMap<>();
        // One JwtAuthenticationManager per trusted static issuer, keyed by iss.
        trustedIssuerJwkSetUris.forEach((iss, jwks) ->
                managers.put(iss, jwtAuthenticationManagerFor(iss, jwks)));
        return new JwtIssuerAuthenticationManagerResolver(managers::get);
    }

    private AuthenticationManager jwtAuthenticationManagerFor(String iss, String jwks) {
        var provider = new JwtAuthenticationProvider(jwtDecoderFor(iss, jwks));
        provider.setJwtAuthenticationConverter(jwtAuthenticationConverter());
        return provider::authenticate;
    }

    private JwtDecoder jwtDecoderFor(String iss, String jwks) {
        var decoder = NimbusJwtDecoder.withJwkSetUri(jwks).jwsAlgorithm(jwsAlgorithm).build();
        decoder.setJwtValidator(JwtValidators.createDefaultWithIssuer(iss));
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

    /**
     * Extract the "iss" claim from a Bearer token without verifying the signature.
     * Safe because the issuer is only used to route to the correct JwtAuthenticationManager,
     * which then verifies the signature against the chosen issuer's JWKS.
     *
     * @param authHeader the full Authorization header value (e.g. "Bearer eyJ...")
     * @return the issuer string, or null if extraction fails
     */
    static String extractIssuerFromToken(String authHeader) {
        if (!hasBearerPrefix(authHeader)) {
            return null;
        }
        String token = authHeader.substring("Bearer ".length());
        if (token.length() > JwtSizeLimitFilter.MAX_JWT_SIZE) {
            return null;
        }
        try {
            return JWTParser.parse(token).getJWTClaimsSet().getIssuer();
        } catch (ParseException e) {
            log.debug("Failed to extract issuer from JWT: {}", e.getMessage());
            return null;
        }
    }

    /**
     * Extract the cluster ID from the JWT audience claim without verifying the signature.
     * Supports both string and array forms of the "aud" claim.
     *
     * <p>If the audience matches the pattern {@code nvcf-icms:{clusterId}}, the cluster ID
     * portion is returned.</p>
     *
     * @param authHeader the full Authorization header value (e.g. "Bearer eyJ...")
     * @return the cluster ID, or null if not found or not in the expected format
     */
    public static String extractClusterIdFromAudience(String authHeader) {
        if (!hasBearerPrefix(authHeader)) {
            return null;
        }
        String token = authHeader.substring("Bearer ".length());
        if (token.length() > JwtSizeLimitFilter.MAX_JWT_SIZE) {
            return null;
        }
        try {
            // Nimbus's getAudience() handles both the single-string and array JSON shapes RFC 7519 allows.
            List<String> audiences = JWTParser.parse(token).getJWTClaimsSet().getAudience();
            if (audiences == null) {
                return null;
            }
            return AuthUtils.extractUniqueClusterIdFromAudience(audiences);
        } catch (ParseException e) {
            log.debug("Failed to extract audience from JWT: {}", e.getMessage());
            return null;
        }
    }

    private static boolean hasBearerPrefix(String authHeader) {
        return authHeader != null
                && authHeader.regionMatches(true, 0, "Bearer ", 0, "Bearer ".length());
    }

    /**
     * Look up a single cluster by ID from the repository.
     *
     * @param clusterId the cluster ID
     * @return the ClusterEntity, or null if not found
     */
    public ClusterEntity lookupClusterById(String clusterId) {
        if (clusterId == null || clusterId.isBlank()) {
            return null;
        }
        return clusterRepository.getClusterInfoByClusterId(clusterId, false).orElse(null);
    }

    /**
     * Resolve authentication for cluster OIDC issuers (K8s PSAT tokens).
     * Extracts the cluster ID from the JWT audience claim, looks up the cluster,
     * and builds a JWT verifier from the stored JWKS.
     *
     * @throws AuthenticationServiceException if no cluster ID in audience, cluster not found, or JWKS parsing fails
     */
    private AuthenticationManager resolveClusterOidcIssuer(HttpServletRequest request) {
        String authHeader = request.getHeader(HttpHeaders.AUTHORIZATION);

        String clusterIdFromAud = extractClusterIdFromAudience(authHeader);
        if (clusterIdFromAud == null) {
            throw new AuthenticationServiceException(
                    "Audience must contain cluster ID in format nvcf-icms:{clusterId}");
        }

        ClusterEntity cluster = lookupClusterById(clusterIdFromAud);
        if (cluster == null || cluster.getJwks() == null || cluster.getJwks().isBlank()) {
            log.debug("No cluster found with valid JWKS for audience-based cluster resolution: {}", clusterIdFromAud);
            throw new AuthenticationServiceException(INVALID_BEARER_TOKEN);
        }

        log.debug("Audience-based cluster resolution for cluster '{}'", clusterIdFromAud);
        try {
            JwtDecoder jwtDecoder = buildJwtDecoderFromJwks(cluster.getJwks());
            JwtAuthenticationConverter converter = new JwtAuthenticationConverter();
            // Grant the exact scope set legacy cluster agents received via
            // their OpenBao-issued OAuth token (see nvcf-openbao-migrations
            // migrations/14_setup_nvca.sh: SCOPES="nvca-cluster,instance_request_update").
            //   - nvca-cluster: /v1/nvca/clusters/{id}/{register,heartbeat,credentials,jwks}
            //   - instance_request_update: /v1/sirs/{id} PUT + /v1/sirs/{id}/{instanceId} POST
            // ReVal's helmreval:render is NOT granted here — NVCA → ReVal uses a
            // separate PSAT with ReVal audience, authorized by ReVal's OIDC middleware.
            converter.setJwtGrantedAuthoritiesConverter(jwt -> List.of(
                    new SimpleGrantedAuthority("nvca-cluster"),
                    new SimpleGrantedAuthority("instance_request_update")
            ));

            JwtAuthenticationProvider provider = new JwtAuthenticationProvider(jwtDecoder);
            provider.setJwtAuthenticationConverter(converter);
            return new ProviderManager(provider);
        } catch (ParseException e) {
            log.error("Failed to parse JWKS for audience-based cluster resolution for cluster {}: {}",
                    clusterIdFromAud, e.getMessage());
            throw new AuthenticationServiceException(INVALID_BEARER_TOKEN, e);
        }
    }

    /**
     * Build a JwtDecoder from a JWKS JSON string.
     * Supports RS256/384/512 and ES256/384/512 algorithms.
     *
     * <p>Returned decoder validates timestamp claims plus subject (PSAT or SPIFFE NVCA)
     * and audience (nvcf-icms:{clusterId} format).</p>
     */
    public JwtDecoder buildJwtDecoderFromJwks(String jwksJson) throws ParseException {
        return buildJwtDecoderFromJwks(jwksJson, nvcaSubjectValidator());
    }

    /**
     * Same as {@link #buildJwtDecoderFromJwks(String)} but with a caller-supplied subject
     * validator, so worker tokens can be verified without widening the NVCA validator.
     */
    public JwtDecoder buildJwtDecoderFromJwks(String jwksJson,
            OAuth2TokenValidator<Jwt> subjectValidator) throws ParseException {
        JWKSet jwkSet = JWKSet.parse(jwksJson);
        ImmutableJWKSet<SecurityContext> jwkSource = new ImmutableJWKSet<>(jwkSet);

        DefaultJWTProcessor<SecurityContext> jwtProcessor = new DefaultJWTProcessor<>();
        JWSVerificationKeySelector<SecurityContext> keySelector =
                new JWSVerificationKeySelector<>(SUPPORTED_JWS_ALGORITHMS, jwkSource);
        jwtProcessor.setJWSKeySelector(keySelector);

        NimbusJwtDecoder decoder = new NimbusJwtDecoder(jwtProcessor);
        decoder.setJwtValidator(new DelegatingOAuth2TokenValidator<>(
                new JwtTimestampValidator(),
                subjectValidator,
                nvcaAudienceValidator()
        ));
        return decoder;
    }

    /**
     * Subject validator for delegated worker tokens: exactly
     * {@code system:serviceaccount:<namespace>:nvcf-worker} (PSAT) or a SPIFFE ID carrying
     * {@code /instance/<id>/worker/<id>} (SPIFFE). Distinct from {@link #nvcaSubjectValidator()},
     * which stays pinned to the {@code nvca} ServiceAccount.
     */
    public static OAuth2TokenValidator<Jwt> workerSubjectValidator() {
        return jwt -> {
            String sub = jwt.getSubject();
            if (sub == null) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token", "Missing sub claim", null));
            }
            boolean ok = AuthUtils.isValidWorkerPsatSubject(sub)
                    || (sub.startsWith("spiffe://") && WORKER_SPIFFE_SEGMENTS.matcher(sub).find());
            if (!ok) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token", "Unauthorized worker subject", null));
            }
            return OAuth2TokenValidatorResult.success();
        };
    }

    private static final java.util.regex.Pattern WORKER_SPIFFE_SEGMENTS =
            java.util.regex.Pattern.compile("/instance/[^/]+/worker/[^/]+");

    /**
     * Validates that the JWT audience claim contains a cluster-specific NVCF-ICMS audience.
     *
     * <p>Accepted pattern: {@code nvcf-icms:{clusterId}} (prefix format only).
     * Bare {@code nvcf-icms} (legacy format) is rejected.</p>
     */
    static OAuth2TokenValidator<Jwt> nvcaAudienceValidator() {
        return jwt -> {
            List<String> audiences = jwt.getAudience();
            if (audiences == null || audiences.isEmpty()) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token", "Missing aud claim", null));
            }
            String clusterId = AuthUtils.extractUniqueClusterIdFromAudience(audiences);
            if (clusterId == null) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token",
                                "Token audience must contain exactly one nvcf-icms:{clusterId} value", null));
            }
            return OAuth2TokenValidatorResult.success();
        };
    }

    /**
     * Validates that the JWT subject claim identifies a Kubernetes service account
     * or SPIFFE workload.
     *
     * <p>Accepted subjects:</p>
     * <ul>
     *   <li>PSAT: {@code system:serviceaccount:<any-ns>:nvca} — ServiceAccount
     *       name must be {@code nvca}; namespace is customer-configurable.
     *       Other SA names (including {@code nvca-operator}, {@code worker},
     *       and {@code nvca-attacker}) are rejected.</li>
     *   <li>SPIFFE: {@code spiffe://...} URI whose path ends with
     *       {@code /nvcf/nvca}.</li>
     * </ul>
     */
    public static OAuth2TokenValidator<Jwt> nvcaSubjectValidator() {
        return jwt -> {
            String sub = jwt.getSubject();
            if (sub == null) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token", "Missing sub claim", null));
            }
            if (!AuthUtils.isValidNvcaWorkloadSubject(sub)) {
                return OAuth2TokenValidatorResult.failure(
                        new OAuth2Error("invalid_token", "Unauthorized subject: " + sub, null));
            }
            return OAuth2TokenValidatorResult.success();
        };
    }

    /**
     * Consolidate every trusted static issuer into a single {@code iss -> jwk-set-uri} map.
     *
     * <p>Order: primary issuer first (service-to-service tokens), then the legacy
     * single admin issuer (folded in so existing {@code admin-issuer-uri}/
     * {@code admin-jwk-set-uri} config keeps working unchanged), then each configured
     * {@code trusted-issuers[]} entry (additional external issuers to trust).
     * Blank entries are skipped; a duplicate issuer with a conflicting JWKS fails fast
     * (an identical duplicate is a no-op).</p>
     */
    private static Map<String, String> buildTrustedIssuerJwkSetUris(
            String issuerUri, String jwkSetUri,
            String adminIssuerUri, String adminJwkSetUri,
            TrustedJwtIssuerProperties trustedIssuerProperties) {
        Map<String, String> issuers = new LinkedHashMap<>();
        // Primary issuer (service-to-service tokens) — always trusted.
        addTrustedIssuer(issuers, issuerUri, jwkSetUri);
        // Legacy single admin issuer, folded in as one trusted entry.
        addTrustedIssuer(issuers, adminIssuerUri, adminJwkSetUri);
        // Additional trusted static issuers (trusted-issuers[]), keyed by iss.
        if (trustedIssuerProperties != null) {
            for (TrustedJwtIssuerProperties.TrustedIssuer entry
                    : trustedIssuerProperties.getTrustedIssuers()) {
                addTrustedIssuer(issuers, entry.getIssuerUri(), entry.getJwkSetUri());
            }
        }
        return Collections.unmodifiableMap(issuers);
    }

    private static void addTrustedIssuer(Map<String, String> issuers, String issuerUri, String jwkSetUri) {
        if (!isConfigured(issuerUri) || !isConfigured(jwkSetUri)) {
            return;
        }
        String previous = issuers.putIfAbsent(issuerUri, jwkSetUri);
        if (previous != null && !previous.equals(jwkSetUri)) {
            throw new IllegalStateException("Conflicting JWKS configured for trusted issuer " + issuerUri);
        }
    }

    private static boolean isConfigured(String value) {
        return value != null && !value.isBlank();
    }

    private boolean isTrustedStaticIssuer(String issuer) {
        return issuer != null && trustedIssuerJwkSetUris.containsKey(issuer);
    }

}
