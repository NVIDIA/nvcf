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
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.MAX_JWKS_SIZE_BYTES;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;
import static com.nvidia.icms.util.AuthUtils.PSAT_AUDIENCE_PREFIX;
import static com.nvidia.icms.util.AuthUtils.computeJwksFingerprint;
import static com.nvidia.icms.util.AuthUtils.isValidNvcaWorkloadSubject;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectResponse;
import com.nvidia.icms.inbound.rest.model.nvca.UpdateJwksRequest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.NvcaService;
import com.nvidia.icms.service.byoc.nvca.ClusterOidcIdentityService;
import com.nvidia.icms.service.byoc.nvca.NvcaNatsAuthorizationService;
import com.nvidia.icms.service.byoc.nvca.NvcaTokenVerificationService;
import com.nvidia.icms.service.heartbeats.NvcaHeartbeatService;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.nio.charset.StandardCharsets;
import java.text.ParseException;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "NVIDIA Cluster Agent")
@RequestMapping(path = "/v1/nvca", produces = APPLICATION_JSON_VALUE)
public class NvcaController {

    @Autowired
    private NvcaService nvcaService;

    @Autowired
    private NvcaHeartbeatService nvcaHeartbeatService;

    @Autowired
    private ClusterOidcIdentityService clusterOidcIdentityService;

    @Autowired
    private NvcaConfigurationProperties nvcaConfig;

    @Autowired
    private NvcaTokenVerificationService nvcaTokenVerificationService;

    @Autowired
    private NvcaNatsAuthorizationService nvcaNatsAuthorizationService;

    /**
     * @return true when OIDC/PSAT cluster identity is enabled in configuration.
     * Self-hosted NVCF deployments enable this; managed NVCF does not.
     */
    private boolean isOidcClusterIdentityEnabled() {
        return nvcaConfig.isOidcClusterIdentityEnabled();
    }

    @PutMapping("clusters/{clusterId}/register")
    @PreAuthorize("hasAuthority('nvca-cluster') or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Cluster Registration",
            description = "API to register a new backend",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "409", description = "Conflict - mismatch with already present data"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = NvcaRegistrationRequest.class)
                    )
            ))
    public NvcaRegistrationResponse registerCluster(
            HttpServletRequest request,
            @Schema(description = "Id of the cluster")
            @PathVariable("clusterId")
            String clusterId,
            @Valid @RequestBody NvcaRegistrationRequest nvcaRegistrationRequest) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        return nvcaService.nvcaClusterRegistration(nvcaRegistrationRequest,
                                                   validateClusterIdWithAuthToken(clusterId),
                                                   auditProps);
    }

    @GetMapping("clusters/{clusterId}/credentials")
    @PreAuthorize("hasAuthority('nvca-cluster') or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Renew credentials for creation and termination queues",
            description = "Request to renew SQS credentials for creation and termination queues",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "409", description = "Conflict - mismatch with already present data"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public NvcaAccessCreds renewAccessCredentials(
            HttpServletRequest request,
            @Schema(description = "Id of the cluster")
            @PathVariable("clusterId")
            String clusterId) {
        return nvcaService.renewAccessCredentials(validateClusterIdWithAuthToken(clusterId));
    }

    @PostMapping("clusters/{clusterId}/heartbeat")
    @PreAuthorize("hasAuthority('nvca-cluster') or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Cluster heartbeat",
            description = "API to send heartbeat of backend",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = NvcaClusterHeartbeatRequest.class)
                    )
            ))
    public NvcaClusterHeartbeatResponse recordNvcaClusterHeartbeat(
            HttpServletRequest request,
            @Schema(description = "Id of the cluster")
            @PathVariable("clusterId")
            String clusterId,
            @Valid @RequestBody NvcaClusterHeartbeatRequest nvcaClusterHeartbeatRequest) {
        return nvcaHeartbeatService.recordClusterHeartbeat(validateClusterIdWithAuthToken(clusterId),
                                               nvcaClusterHeartbeatRequest);
    }

    @PutMapping("clusters/{clusterId}/jwks")
    @PreAuthorize("hasAuthority('nvca-cluster') or hasAuthority('apikey:nvca-cluster') or hasAuthority('spiffe-authenticated') or hasAuthority('cluster-management')")
    @Operation(summary = "Update cluster JWKS",
            description = "Push updated JWKS for cert rotation",
            responses = {
                    @ApiResponse(responseCode = "200", description = "JWKS updated successfully"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Invalid JWKS format or missing field"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Cluster not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "413", description = "JWKS payload too large")
            })
    public ResponseEntity<Void> updateClusterJwks(
            @PathVariable("clusterId") String clusterId,
            @Valid @RequestBody UpdateJwksRequest body) {
        if (!isOidcClusterIdentityEnabled()) {
            return ResponseEntity.notFound().build();
        }
        String jwks = body.getJwks();

        // Reject oversized JWKS payloads
        if (jwks.getBytes(StandardCharsets.UTF_8).length > MAX_JWKS_SIZE_BYTES) {
            return ResponseEntity.status(413).build();
        }

        // Validate JWKS format before storing
        try {
            JWKSet.parse(jwks);
        } catch (ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }

        // Admin break-glass path: callers with cluster-management authority (e.g. nvcf-cli
        // cluster rotate --force) are trusted to update any cluster's JWKS and bypass the
        // per-cluster JWT identity-binding check. For PSAT/SPIFFE callers, the JWT must be
        // bound to the path clusterId.
        Authentication auth = SecurityContextHolder.getContext().getAuthentication();
        boolean isAdmin = auth != null && auth.getAuthorities().stream()
                .anyMatch(a -> "cluster-management".equals(a.getAuthority()));
        String validatedClusterId = isAdmin ? clusterId : validateClusterIdWithAuthToken(clusterId);

        ClusterEntity cluster = clusterOidcIdentityService.findByClusterId(validatedClusterId)
                .orElseThrow(() -> new IcmsNotFoundException(
                        String.format("Cluster %s not found", validatedClusterId)));

        String fingerprint;
        try {
            fingerprint = computeJwksFingerprint(jwks);
        } catch (ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }

        clusterOidcIdentityService.validateFingerprintAvailable(fingerprint, validatedClusterId);
        // Normalize before persisting: JWT verification compares `iss` to the
        // stored issuer with exact-string equality (see
        // `validateClusterIdWithOidcWorkloadToken`), so any leading/trailing
        // whitespace from a misbehaving client would break auth post-rotate.
        // `trimToNull` collapses null/whitespace-only values to null, which
        // keeps the "omit means keep existing" semantics intact while
        // sanitizing genuinely supplied values.
        String requestedOidcIssuer = StringUtils.trimToNull(body.getOidcIssuer());
        String effectiveOidcIssuer = requestedOidcIssuer != null
                ? requestedOidcIssuer
                : cluster.getOidcIssuer();
        clusterOidcIdentityService.updateOidcIdentity(
                validatedClusterId,
                jwks,
                effectiveOidcIssuer,
                fingerprint);

        log.info("Updated JWKS for cluster {}", validatedClusterId);
        return ResponseEntity.ok().build();
    }

    @PostMapping("tokens/introspect")
    @Operation(summary = "Token introspection (RFC 7662)",
            description = "Verify a PSAT or SPIFFE JWT and return resolved cluster identity. "
                    + "Public endpoint — no authentication required. "
                    + "Used by NATS auth callout and ReVal to delegate JWT verification to ICMS.",
            responses = {
                    @ApiResponse(responseCode = "200", description = "Introspection result (active or inactive)"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Missing or empty token"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "431", description = "JWT too large")
            })
    public ResponseEntity<TokenIntrospectResponse> introspectToken(
            @Valid @RequestBody TokenIntrospectRequest request) {
        if (!isOidcClusterIdentityEnabled()) {
            return ResponseEntity.notFound().build();
        }

        NvcaTokenVerificationService.Outcome outcome = nvcaTokenVerificationService.verify(request.getToken());

        if (outcome.getReason() == NvcaTokenVerificationService.RejectReason.TOKEN_TOO_LARGE) {
            return ResponseEntity.status(431).body(TokenIntrospectResponse.inactive(outcome.getErrorMessage()));
        }
        if (!outcome.isActive()) {
            // RFC 7662: every non-token-size failure returns active=false at HTTP 200.
            return ResponseEntity.ok(TokenIntrospectResponse.inactive(outcome.getErrorMessage()));
        }

        var jwt = outcome.getJwt();
        String subject = jwt.getSubject();
        String tokenType = (subject != null && subject.startsWith("spiffe://")) ? "spiffe" : "psat";
        String audience = jwt.getAudience() != null && !jwt.getAudience().isEmpty()
                ? jwt.getAudience().get(0) : null;

        return ResponseEntity.ok(TokenIntrospectResponse.builder()
                .active(true)
                .sub(subject)
                .aud(audience)
                .iss(jwt.getIssuer() != null ? jwt.getIssuer().toString() : null)
                .clusterId(outcome.getClusterId())
                .tokenType(tokenType)
                .build());
    }

    @PostMapping("nats-authorize")
    @Operation(summary = "NATS auth-callout webhook",
            description = "Webhook-contract endpoint consumed by the generic `webhook` plugin in "
                    + "nvcf-nats-auth-callout-service. Verifies a PSAT/SPIFFE JWT using the same "
                    + "pipeline as /introspect, then returns NATS user permissions scoped to the "
                    + "verified clusterId. Request/response field names match the plugin's "
                    + "internal/plugins/webhook contract exactly.",
            responses = {
                    @ApiResponse(responseCode = "200", description = "Authorized; permissions in body"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400",
                            description = "Missing account/pluginName/payload"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401",
                            description = "Token verification failed"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403",
                            description = "Token valid but caller not authorized for NATS"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404",
                            description = "Feature flag disabled"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "431",
                            description = "JWT payload too large")
            })
    public ResponseEntity<NatsAuthorizeResponse> natsAuthorize(@Valid @RequestBody NatsAuthorizeRequest request) {
        if (!isOidcClusterIdentityEnabled()) {
            return ResponseEntity.notFound().build();
        }
        if (!isAuthorizedNatsWebhookRequest(request)) {
            log.warn("nats-authorize rejected: unexpected account or pluginName");
            return ResponseEntity.status(403).build();
        }

        NvcaTokenVerificationService.Outcome outcome = nvcaTokenVerificationService.verify(request.getPayload());
        if (!outcome.isActive()) {
            // Map rejection reasons to status codes the webhook plugin understands.
            // The plugin treats 401/403 as terminal (no retry) and 431 as "request
            // too large" — anything else as retriable client/server error.
            int status = switch (outcome.getReason()) {
                case TOKEN_TOO_LARGE -> 431;
                case MISSING_TOKEN, INVALID_AUDIENCE -> 400;
                case UNKNOWN_CLUSTER, SIGNATURE_INVALID -> 401;
            };
            log.debug("nats-authorize rejected (reason={}): {}", outcome.getReason(), outcome.getErrorMessage());
            return ResponseEntity.status(status).build();
        }

        try {
            NatsAuthorizeResponse response = nvcaNatsAuthorizationService.buildResponse(outcome.getClusterId());
            return ResponseEntity.ok(response);
        } catch (IllegalArgumentException | IllegalStateException e) {
            // Fail closed with 403 so the webhook plugin rejects the connect for
            // malformed verified clusterIds or unsafe/missing permission config.
            log.warn("nats-authorize: refusing to issue permissions for verified token: {}", e.getMessage());
            return ResponseEntity.status(403).build();
        }
    }

    private boolean isAuthorizedNatsWebhookRequest(NatsAuthorizeRequest request) {
        NvcaConfigurationProperties.NatsAuth cfg = nvcaConfig.getNatsAuth();
        if (!StringUtils.equals(cfg.getAccount(), request.getAccount())) {
            return false;
        }
        List<String> allowedPluginNames = cfg.getAllowedPluginNames();
        return allowedPluginNames != null && allowedPluginNames.stream()
                .anyMatch(allowed -> StringUtils.equals(allowed, request.getPluginName()));
    }

    // clientId from token should match with provided clusterId
    private String validateClusterIdWithAuthToken(String clusterId) {

        Authentication auth = SecurityContextHolder.getContext().getAuthentication();
        if (auth instanceof JwtAuthenticationToken jwtAuth && isOidcWorkloadToken(jwtAuth)) {
            return validateClusterIdWithOidcWorkloadToken(clusterId, jwtAuth);
        }

        String clientId = AuthUtils.getSubOrClusterIdFromSecurityContext();

        // Direct comparison for ApiKey
        if (clusterId.equals(clientId)) {
            return clusterId;
        }

        // Hashed UUID comparison for OAuth token
        String hashedClientId = getClusterIdFromAuthClientId(clientId);
        if (clusterId.equals(hashedClientId)) {
            log.info("{} authClientId has {} hashedClientId", clientId, hashedClientId);
            return hashedClientId;
        }

        String err = String.format(
                "%s provided clusterId is not matching with %s clientId from auth token",
                clusterId, clientId);
        log.error(err);
        throw new IcmsConflictException(err);
    }

    private boolean isOidcWorkloadToken(JwtAuthenticationToken jwtAuth) {
        String subject = jwtAuth.getToken().getSubject();
        List<String> audiences = jwtAuth.getToken().getAudience();
        boolean hasWorkloadSubject = StringUtils.isNotBlank(subject)
                && (subject.startsWith(AuthUtils.PSAT_SUBJECT_PREFIX)
                        || subject.startsWith("spiffe://"));
        boolean hasClusterAudience = audiences != null && audiences.stream()
                .anyMatch(aud -> aud != null && aud.startsWith(PSAT_AUDIENCE_PREFIX));
        return hasWorkloadSubject || hasClusterAudience;
    }

    private String validateClusterIdWithOidcWorkloadToken(
            String clusterId, JwtAuthenticationToken jwtAuth) {
        // PSAT/SPIFFE: look up cluster by OIDC issuer claim.
        // For K8s service account tokens, the sub is like "system:serviceaccount:nvca-system:nvca"
        // and doesn't contain the cluster UUID. Resolve cluster from the JWT issuer instead.
        String issuer = jwtAuth.getToken().getClaimAsString("iss");
        String subject = jwtAuth.getToken().getClaimAsString("sub");
        List<String> audiences = jwtAuth.getToken().getClaimAsStringList("aud");

        if (!isValidNvcaWorkloadSubject(subject)) {
            log.warn("PSAT token subject does not match expected pattern: {}",
                    subject != null ? subject.substring(0, Math.min(subject.length(), 40)) : "null");
            throw new IcmsConflictException("PSAT token subject does not match expected service account pattern");
        }

        String expectedAudience = PSAT_AUDIENCE_PREFIX + clusterId;
        boolean audienceMatchesPath = audiences != null && audiences.stream()
                .anyMatch(expectedAudience::equals);
        if (!audienceMatchesPath) {
            log.warn("PSAT token audience does not match path clusterId");
            throw new IcmsConflictException("PSAT token audience does not match path clusterId");
        }

        if (StringUtils.isNotBlank(issuer)) {
            // Look up oidc_issuer from the cluster row to verify the caller
            // owns this cluster.
            String storedIssuer = clusterOidcIdentityService.findByClusterId(clusterId)
                    .map(ClusterEntity::getOidcIssuer)
                    .orElse(null);
            if (storedIssuer != null && issuer.equals(storedIssuer)) {
                log.info("OIDC workload token validated for cluster {} via OIDC issuer", clusterId);
                return clusterId;
            }
        }

        String err = String.format(
                "%s provided clusterId is not matching with OIDC workload token issuer %s",
                clusterId, issuer);
        log.error(err);
        throw new IcmsConflictException(err);
    }
}
