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
package com.nvidia.icms.service.workers;

import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.stereotype.Service;

/**
 * Verification pipeline for worker-presented PSAT / SPIFFE tokens.
 *
 * <p>Delegates cluster JWKS resolution and signature verification to
 * {@link ClusterOIDCTokenVerificationService}, then applies worker-specific checks:
 * subject format validation, instance/worker-ID derivation, and identity-set
 * matching against the registration stored during instance status updates.</p>
 *
 * <p>Returns a discriminated {@link Outcome} rather than throwing so the
 * controller can map each failure mode to the correct HTTP status code.</p>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerTokenVerificationService {

    public static final String TOKEN_TYPE_PSAT = "psat";
    public static final String TOKEN_TYPE_SPIFFE = "spiffe";

    private static final String WORKER_SA_PREFIX = "nvcf-worker-";
    private static final Pattern SPIFFE_INSTANCE_WORKER =
            Pattern.compile(".*/instance/([^/]+)/worker/([^/]+).*");

    private final ClusterOIDCTokenVerificationService nvcaTokenVerificationService;
    private final WorkerIdentifierService workerIdentifierService;

    /**
     * Verify a raw worker JWT (no {@code Bearer } prefix).
     *
     * @param token compact-serialized JWT
     * @return verification outcome
     */
    public Outcome verify(String token) {
        // Step 1: cluster JWKS resolution + signature verify (also covers size + audience checks)
        ClusterOIDCTokenVerificationService.Outcome base = nvcaTokenVerificationService.verify(token);
        if (!base.isActive()) {
            return Outcome.reject(base.getReason(), base.getErrorMessage());
        }

        Jwt jwt = base.getJwt();
        String clusterId = base.getClusterId();
        String sub = jwt.getSubject();

        // Step 2: discriminate token type and derive instance/worker IDs
        if (sub != null && sub.startsWith("system:serviceaccount:")) {
            return verifyPsat(jwt, clusterId, sub);
        } else if (sub != null && sub.startsWith("spiffe://")) {
            return verifySpiffe(jwt, clusterId, sub);
        } else {
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "unrecognized subject format");
        }
    }

    @SuppressWarnings("unchecked")
    private Outcome verifyPsat(Jwt jwt, String clusterId, String sub) {
        // Derive instance ID from ServiceAccount name embedded in sub
        // sub format: system:serviceaccount:<namespace>:<saName>
        int lastColon = sub.lastIndexOf(':');
        if (lastColon < 0) {
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "malformed SAT subject");
        }
        String saName = sub.substring(lastColon + 1);
        if (!saName.startsWith(WORKER_SA_PREFIX)) {
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "sub does not match worker SA pattern");
        }
        String instanceId = saName.substring(WORKER_SA_PREFIX.length());

        // Extract pod name and UID from the kubernetes.io claim
        Map<String, Object> k8sClaim = (Map<String, Object>) jwt.getClaims().get("kubernetes.io");
        Map<String, Object> podClaim = k8sClaim != null
                ? (Map<String, Object>) k8sClaim.get("pod") : null;
        String podName = podClaim != null ? (String) podClaim.get("name") : null;
        String podUid = podClaim != null ? (String) podClaim.get("uid") : null;
        if (podName == null || podUid == null) {
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "kubernetes.io pod claims missing");
        }

        // Step 3: look up registered worker-identity set
        Optional<WorkerIdentifierRecord> record =
                workerIdentifierService.findWorkerIdentifiers(clusterId, instanceId);
        if (record.isEmpty()) {
            log.debug("No worker identifiers registered for cluster={} instance={}",
                    clusterId, instanceId);
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "no worker identifiers registered for instance");
        }

        WorkerIdentifierRecord reg = record.get();

        // Subject must match the registered sub
        if (!sub.equals(reg.getSub())) {
            log.debug("SAT sub mismatch for cluster={} instance={}", clusterId, instanceId);
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "worker identity not in registered set");
        }

        // Pod name + UID must match at least one registered identifier
        List<WorkerIdentifierUdt> identifiers = reg.getIdentifiers();
        boolean matched = identifiers != null && identifiers.stream()
                .anyMatch(wi -> podName.equals(wi.getName()) && podUid.equals(wi.getUid()));
        if (!matched) {
            log.debug("SAT pod identity mismatch for cluster={} instance={}", clusterId, instanceId);
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "worker identity not in registered set");
        }

        String aud = firstAudience(jwt);
        return Outcome.active(jwt, clusterId, instanceId, podName, TOKEN_TYPE_PSAT, aud);
    }

    private Outcome verifySpiffe(Jwt jwt, String clusterId, String sub) {
        // Derive instance ID and worker ID from SPIFFE ID path
        Matcher m = SPIFFE_INSTANCE_WORKER.matcher(sub);
        if (!m.matches()) {
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "SPIFFE ID does not contain /instance/{id}/worker/{wid}");
        }
        String instanceId = m.group(1);
        String workerId = m.group(2);

        // Look up registered worker-identity set
        Optional<WorkerIdentifierRecord> record =
                workerIdentifierService.findWorkerIdentifiers(clusterId, instanceId);
        if (record.isEmpty()) {
            log.debug("No worker identifiers registered for cluster={} instance={}",
                    clusterId, instanceId);
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "no worker identifiers registered for instance");
        }

        WorkerIdentifierRecord reg = record.get();

        // SPIFFE sub must appear as the name of at least one registered identifier
        List<WorkerIdentifierUdt> identifiers = reg.getIdentifiers();
        boolean matched = identifiers != null && identifiers.stream()
                .anyMatch(wi -> sub.equals(wi.getName()));
        if (!matched) {
            log.debug("SPIFFE identity not in registered set for cluster={} instance={}",
                    clusterId, instanceId);
            return Outcome.reject(ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                    "worker identity not in registered set");
        }

        String aud = firstAudience(jwt);
        return Outcome.active(jwt, clusterId, instanceId, workerId, TOKEN_TYPE_SPIFFE, aud);
    }

    private static String firstAudience(Jwt jwt) {
        List<String> audiences = jwt.getAudience();
        return (audiences != null && !audiences.isEmpty()) ? audiences.get(0) : null;
    }

    /** Result of {@link #verify(String)}. */
    public static final class Outcome {
        private final Jwt jwt;
        private final String clusterId;
        private final String instanceId;
        private final String workerId;
        private final String tokenType;
        private final String audience;
        private final ClusterOIDCTokenVerificationService.RejectReason reason;
        private final String errorMessage;

        private Outcome(Jwt jwt, String clusterId, String instanceId, String workerId,
                String tokenType, String audience,
                ClusterOIDCTokenVerificationService.RejectReason reason, String errorMessage) {
            this.jwt = jwt;
            this.clusterId = clusterId;
            this.instanceId = instanceId;
            this.workerId = workerId;
            this.tokenType = tokenType;
            this.audience = audience;
            this.reason = reason;
            this.errorMessage = errorMessage;
        }

        public static Outcome active(Jwt jwt, String clusterId, String instanceId, String workerId,
                String tokenType, String audience) {
            return new Outcome(jwt, clusterId, instanceId, workerId, tokenType, audience,
                    null, null);
        }

        public static Outcome reject(ClusterOIDCTokenVerificationService.RejectReason reason, String message) {
            return new Outcome(null, null, null, null, null, null, reason, message);
        }

        public boolean isActive() { return jwt != null; }
        public Jwt getJwt() { return jwt; }
        public String getClusterId() { return clusterId; }
        public String getInstanceId() { return instanceId; }
        public String getWorkerId() { return workerId; }
        public String getTokenType() { return tokenType; }
        public String getAudience() { return audience; }
        public ClusterOIDCTokenVerificationService.RejectReason getReason() { return reason; }
        public String getErrorMessage() { return errorMessage; }
    }
}
