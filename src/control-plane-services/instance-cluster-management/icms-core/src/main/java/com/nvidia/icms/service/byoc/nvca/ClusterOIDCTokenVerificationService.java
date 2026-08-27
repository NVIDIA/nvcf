/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.service.byoc.nvca;

import com.nvidia.icms.configuration.security.AuthManagerResolver;
import com.nvidia.icms.configuration.security.JwtSizeLimitFilter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.nio.charset.StandardCharsets;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.stereotype.Service;

/**
 * Shared verification pipeline for PSAT / SPIFFE NVCA tokens.
 *
 * <p>Both {@code POST /v1/nvca/tokens/introspect} / {@code POST /v1/si/oidc/tokens/introspect}
 * (RFC 7662, consumed by ReVal)
 * and {@code POST /v1/nvca/nats-authorize} / {@code POST /v1/si/oidc/nats-authorize}
 * (NATS auth-callout webhook contract,
 * consumed by the {@code webhook} plugin in {@code nvcf-nats-auth-callout-service})
 * run a token through this service so that subject/audience rules, JWKS
 * lookup, and signature verification stay consistent across both surfaces.</p>
 *
 * <p>Returns a discriminated {@link Outcome} rather than throwing on verification
 * failure, because the two callers render failure differently — introspect
 * returns {@code active=false} at HTTP 200, while nats-authorize returns 4xx
 * to signal the auth-callout webhook plugin that the request is unauthorized.</p>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class ClusterOIDCTokenVerificationService {

    private final AuthManagerResolver authManagerResolver;
    private final ClusterOidcIdentityService clusterOidcIdentityService;

    /**
     * Verify a raw JWT string (no {@code Bearer } prefix).
     *
     * @param token the compact-serialized JWT
     * @return verification outcome (see {@link Outcome})
     */
    public Outcome verify(String token) {
        if (token == null || token.isBlank()) {
            return Outcome.reject(RejectReason.MISSING_TOKEN, "token is required");
        }
        if (token.getBytes(StandardCharsets.UTF_8).length > JwtSizeLimitFilter.MAX_JWT_SIZE) {
            return Outcome.reject(RejectReason.TOKEN_TOO_LARGE,
                    "JWT exceeds " + JwtSizeLimitFilter.MAX_JWT_SIZE + " byte limit");
        }

        // Unsigned parse of the audience to pick the right JWKS. Any signature
        // error surfaces below when the decoder runs against that JWKS.
        String clusterIdFromAud = AuthManagerResolver.extractClusterIdFromAudience("Bearer " + token);
        if (clusterIdFromAud == null) {
            return Outcome.reject(RejectReason.INVALID_AUDIENCE,
                    "Audience must contain cluster ID in format nvcf-icms:{clusterId}");
        }

        Optional<ClusterEntity> cluster = clusterOidcIdentityService.findByClusterId(clusterIdFromAud);
        if (cluster.isEmpty() || cluster.get().getJwks() == null || cluster.get().getJwks().isBlank()) {
            // Do not echo the audience value back to unauthenticated callers —
            // that would let anyone enumerate registered clusters by varying
            // the aud claim and watching the response text. The clusterId is
            // only on the server-side log.
            log.debug("verify: no cluster with valid JWKS for clusterIdFromAud={}", clusterIdFromAud);
            return Outcome.reject(RejectReason.UNKNOWN_CLUSTER,
                    "No cluster found with valid JWKS for the provided audience");
        }

        try {
            JwtDecoder decoder = authManagerResolver.buildJwtDecoderFromJwks(cluster.get().getJwks());
            Jwt jwt = decoder.decode(token);
            return Outcome.active(jwt, clusterIdFromAud);
        } catch (Exception e) {
            // Full exception (key selector / parse internals) goes to the log for
            // operators; the caller-facing error is generic to avoid leaking
            // algorithm / JWK-parse detail.
            log.debug("Token verification failed for cluster {}", clusterIdFromAud, e);
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "JWT verification failed");
        }
    }

    /** Result of {@link #verify(String)}. Exactly one of {@code jwt} or {@code reason} is set. */
    public static final class Outcome {
        private final Jwt jwt;
        private final String clusterId;
        private final RejectReason reason;
        private final String errorMessage;

        private Outcome(Jwt jwt, String clusterId, RejectReason reason, String errorMessage) {
            this.jwt = jwt;
            this.clusterId = clusterId;
            this.reason = reason;
            this.errorMessage = errorMessage;
        }

        public static Outcome active(Jwt jwt, String clusterId) {
            return new Outcome(jwt, clusterId, null, null);
        }

        public static Outcome reject(RejectReason reason, String message) {
            return new Outcome(null, null, reason, message);
        }

        public boolean isActive() { return jwt != null; }
        public Jwt getJwt() { return jwt; }
        public String getClusterId() { return clusterId; }
        public RejectReason getReason() { return reason; }
        public String getErrorMessage() { return errorMessage; }
    }

    /** Why a token was rejected. Callers map to HTTP status codes. */
    public enum RejectReason {
        MISSING_TOKEN,
        TOKEN_TOO_LARGE,
        INVALID_AUDIENCE,
        UNKNOWN_CLUSTER,
        SIGNATURE_INVALID
    }
}
