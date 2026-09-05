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
package com.nvidia.icms.util;

import static com.nvidia.icms.outbound.apikeys.ApiKeysService.API_KEY_FAILURE_ERROR;
import static com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult.API_KEY_SCOPE_PREFIX;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.icms.errors.IcmsAuthenticationException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.text.ParseException;
import java.util.List;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;

@Slf4j
public class AuthUtils {

    public static final String API_KEY_RESOURCE_TYPE_CLUSTER = "cluster";

    /** Audience prefix for cluster-specific PSAT tokens (format: nvcf-icms:{clusterId}). */
    public static final String PSAT_AUDIENCE_PREFIX = "nvcf-icms:";

    /** Expected PSAT subject prefix for Kubernetes service accounts. */
    public static final String PSAT_SUBJECT_PREFIX = "system:serviceaccount:";

    private static final String EXPECTED_PSAT_SA_NAME = "nvca";

    private static final String EXPECTED_SPIFFE_NVCA_SEGMENT = "/nvcf/nvca";

    /**
     * Compute a deterministic fingerprint of a JWKS JSON string.
     * Uses RFC 7638 JWK thumbprints sorted and hashed with SHA-256,
     * so the result is stable regardless of JSON formatting or key order.
     *
     * @param jwksJson the JWKS JSON string
     * @return hex-encoded SHA-256 fingerprint
     * @throws ParseException if the JWKS JSON cannot be parsed
     */
    public static String computeJwksFingerprint(String jwksJson) throws ParseException {
        JWKSet jwkSet = JWKSet.parse(jwksJson);
        try {
            String normalized = jwkSet.getKeys().stream()
                    .map(jwk -> {
                        try {
                            return jwk.computeThumbprint().toString();
                        } catch (JOSEException e) {
                            throw new RuntimeException(e);
                        }
                    })
                    .sorted()
                    .collect(Collectors.joining(","));
            return sha256Hex(normalized);
        } catch (RuntimeException e) {
            if (e.getCause() instanceof JOSEException) {
                throw new ParseException(
                        "Failed to compute JWK thumbprint: " + e.getCause().getMessage(), 0);
            }
            throw e;
        }
    }

    private static String sha256Hex(String input) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] hash = digest.digest(input.getBytes(StandardCharsets.UTF_8));
            StringBuilder hexString = new StringBuilder(2 * hash.length);
            for (byte b : hash) {
                String hex = Integer.toHexString(0xff & b);
                if (hex.length() == 1) {
                    hexString.append('0');
                }
                hexString.append(hex);
            }
            return hexString.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new RuntimeException("SHA-256 algorithm not available", e);
        }
    }

    /**
     * Returns true iff {@code sub} has the shape
     * {@code system:serviceaccount:<namespace>:nvca}. The namespace is
     * customer-configurable; the service account name must be exactly
     * {@code nvca}.
     */
    public static boolean isValidPsatNvcaSubject(String sub) {
        if (sub == null || !sub.startsWith(PSAT_SUBJECT_PREFIX)) {
            return false;
        }
        String rest = sub.substring(PSAT_SUBJECT_PREFIX.length());
        int sep = rest.indexOf(':');
        if (sep <= 0 || sep != rest.lastIndexOf(':')) {
            return false;
        }
        String sa = rest.substring(sep + 1);
        return EXPECTED_PSAT_SA_NAME.equals(sa);
    }

    /** ServiceAccount name of every delegated worker; unique per instance namespace. */
    public static final String WORKER_SA_NAME = "nvcf-worker";

    /**
     * True for {@code system:serviceaccount:<namespace>:nvcf-worker} with a non-empty
     * namespace and nothing else.
     */
    public static boolean isValidWorkerPsatSubject(String sub) {
        String namespace = workerSubjectNamespace(sub);
        return namespace != null;
    }

    /** Namespace segment of a worker PSAT subject, or null when the subject is not one. */
    public static String workerSubjectNamespace(String sub) {
        if (sub == null || !sub.startsWith(PSAT_SUBJECT_PREFIX)) {
            return null;
        }
        String rest = sub.substring(PSAT_SUBJECT_PREFIX.length());
        int sep = rest.indexOf(':');
        if (sep <= 0 || sep != rest.lastIndexOf(':')) {
            return null;
        }
        return WORKER_SA_NAME.equals(rest.substring(sep + 1)) ? rest.substring(0, sep) : null;
    }

    /**
     * True when the current principal is a compute cluster (NVCA over cluster OIDC/PSAT or a
     * cluster-scoped API key) rather than a control-plane service identity.
     */
    public static boolean isClusterPrincipal() {
        Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
        if (authentication == null) {
            return false;
        }
        for (GrantedAuthority authority : authentication.getAuthorities()) {
            String a = authority.getAuthority();
            if ("nvca-cluster".equals(a) || "apikey:nvca-cluster".equals(a)) {
                return true;
            }
        }
        return false;
    }

    public static boolean isValidNvcaWorkloadSubject(String sub) {
        return isValidPsatNvcaSubject(sub)
                || (sub != null && sub.startsWith("spiffe://")
                        && sub.endsWith(EXPECTED_SPIFFE_NVCA_SEGMENT));
    }

    public static String getSubFromSecurityContext() {
        try {
            Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
            return authentication.getName();
        } catch (Exception e) {
            String errMsg = String.format("Failed to get auth details, error %s", e.getMessage());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

    public static String getSubOrClusterIdFromSecurityContext() {
        try {
            Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
            // Get "clusterId" from Authz policy response (non-BYOC cluster flow).
            if (hasApiKeyAuthority(authentication)) {
                return getClusterIdFromApiKey();
            }
            // PSAT / OIDC cluster identity: the audience claim carries the
            // cluster ID as nvcf-icms:{clusterId}. The JWT was already verified
            // against that cluster's stored JWKS by AuthManagerResolver, so the
            // audience is trusted here. For legacy NVCA JWT tokens the sub was
            // the cluster ID directly; for PSAT tokens the sub is the K8s
            // ServiceAccount subject (system:serviceaccount:nvca-system:nvca)
            // which is the same string across every cluster — using it as the
            // cluster ID lookup key produced 404s on every downstream SIS call
            // that identifies the caller by clusterId (/v1/sirs/{id} ack,
            // instance status updates, etc.).
            String clusterIdFromAud = extractClusterIdFromJwtAudience(authentication);
            if (clusterIdFromAud != null) {
                return clusterIdFromAud;
            }
            // Else get "sub" from JWT token (legacy managed-NVCA flow).
            return getSubFromSecurityContext(authentication);
        } catch (Exception e) {
            String errMsg = String.format("Failed to get auth details, error: %s", e.getMessage());
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

    /**
     * Extract the cluster ID from a PSAT JWT's audience claim if it is in the
     * nvcf-icms:{clusterId} format. Returns null for non-PSAT principals
     * (ApiKey, legacy JWT) so callers fall through to sub-based resolution.
     */
    private static String extractClusterIdFromJwtAudience(Authentication authentication) {
        if (authentication == null) {
            return null;
        }
        Object principal = authentication.getPrincipal();
        if (!(principal instanceof Jwt jwt)) {
            return null;
        }
        List<String> audiences = jwt.getAudience();
        return extractUniqueClusterIdFromAudience(audiences);
    }

    public static String extractUniqueClusterIdFromAudience(List<String> audiences) {
        if (audiences == null) {
            return null;
        }
        String clusterId = null;
        for (String aud : audiences) {
            if (aud == null || !aud.startsWith(PSAT_AUDIENCE_PREFIX)) {
                continue;
            }
            String candidate = aud.substring(PSAT_AUDIENCE_PREFIX.length());
            if (candidate.isBlank()) {
                continue;
            }
            if (clusterId == null) {
                clusterId = candidate;
            } else if (!clusterId.equals(candidate)) {
                return null;
            }
        }
        return clusterId;
    }

    public static String getSubFromSecurityContext(Authentication authentication) {
        return authentication.getName();
    }

    private static boolean hasApiKeyAuthority(Authentication authentication) {
        for (GrantedAuthority authority : authentication.getAuthorities()) {
            if (authority.getAuthority().startsWith(API_KEY_SCOPE_PREFIX)) {
                return true;
            }
        }
        return false;
    }

    public static String getClusterIdFromApiKey() {
        Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication.getPrincipal() instanceof OAuth2AuthenticatedPrincipal principal)) {
            logApiKeyFailureError("ClusterId not found in ApiKey");
            throw new IcmsAuthenticationException(API_KEY_FAILURE_ERROR);
        }
        if (principal.getAttribute(
                ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            var policy = policyResult.getPolicy();
            var clusterResource = policy.getResources().stream()
                    .filter(resource -> API_KEY_RESOURCE_TYPE_CLUSTER.equals(resource.getType()))
                    .findFirst();
            return clusterResource.map(ApiKeyValidationResult.Resource::getId)
                    .orElseThrow(() -> {
                                logApiKeyFailureError("ClusterId not found in ApiKey");
                                return new IcmsAuthenticationException(API_KEY_FAILURE_ERROR);
                            }
                    );
        }

        logApiKeyFailureError("Policy attribute not found");
        throw new IcmsAuthenticationException(API_KEY_FAILURE_ERROR);
    }

    private static void logApiKeyFailureError(String error) {
        log.error("Apikey validation failure: className: AuthUtils methodName: getClusterIdFromApiKey error: {}", error);
    }
}
