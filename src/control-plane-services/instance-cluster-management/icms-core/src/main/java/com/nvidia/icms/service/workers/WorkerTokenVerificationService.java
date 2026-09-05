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

import com.nvidia.icms.configuration.security.AuthManagerResolver;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierUdt;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService.RejectReason;
import com.nvidia.icms.util.AuthUtils;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Builder;
import lombok.Getter;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.stereotype.Service;

/**
 * Worker-token introspection core.
 *
 * <ol>
 *   <li>Cluster resolution from the signed audience and signature verification against that
 *       cluster's JWKS, with the worker subject validator.</li>
 *   <li>Identity match: PSAT tokens are resolved to an instance through the registered
 *       ServiceAccount UID and must match the registered subject, namespace, SA UID, and one
 *       registered pod (name and UID). SPIFFE tokens must equal a registered identifier and
 *       carry the resolved cluster in their path.</li>
 *   <li>Workload binding: the instance's request record, which must belong to the token's
 *       cluster, supplies the request, function/version or task/NCA identifiers the relying
 *       party binds the request to.</li>
 * </ol>
 *
 * <p>The raw JWT is the only worker-supplied identity assertion; nothing else from the caller
 * influences the outcome.</p>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class WorkerTokenVerificationService {

    public static final String TOKEN_TYPE_PSAT = "psat";
    public static final String TOKEN_TYPE_SPIFFE = "spiffe";

    private static final Pattern SPIFFE_INSTANCE_WORKER =
            Pattern.compile("/instance/([^/]+)/worker/([^/]+)");

    private final ClusterOIDCTokenVerificationService nvcaTokenVerificationService;
    private final WorkerIdentifierService workerIdentifierService;
    private final InstanceV2Repository instanceV2Repository;
    private final InstanceRequestV2Repository instanceRequestV2Repository;

    public Outcome verify(String token) {
        ClusterOIDCTokenVerificationService.Outcome base = nvcaTokenVerificationService.verify(
                token, AuthManagerResolver.workerSubjectValidator());
        if (!base.isActive()) {
            return Outcome.reject(base.getReason(), base.getErrorMessage());
        }

        Jwt jwt = base.getJwt();
        String clusterId = base.getClusterId();
        String sub = jwt.getSubject();

        if (sub != null && sub.startsWith(AuthUtils.PSAT_SUBJECT_PREFIX)) {
            return verifyPsat(jwt, clusterId, sub);
        } else if (sub != null && sub.startsWith("spiffe://")) {
            return verifySpiffe(jwt, clusterId, sub);
        }
        return Outcome.reject(RejectReason.SIGNATURE_INVALID, "unrecognized subject format");
    }

    @SuppressWarnings("unchecked")
    private Outcome verifyPsat(Jwt jwt, String clusterId, String sub) {
        String namespace = AuthUtils.workerSubjectNamespace(sub);
        if (namespace == null) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "sub is not a worker ServiceAccount");
        }

        Map<String, Object> k8sClaim = asMap(jwt.getClaims().get("kubernetes.io"));
        Map<String, Object> podClaim = k8sClaim != null ? asMap(k8sClaim.get("pod")) : null;
        Map<String, Object> saClaim = k8sClaim != null ? asMap(k8sClaim.get("serviceaccount")) : null;
        String claimNamespace = k8sClaim != null ? asString(k8sClaim.get("namespace")) : null;
        String podName = podClaim != null ? asString(podClaim.get("name")) : null;
        String podUid = podClaim != null ? asString(podClaim.get("uid")) : null;
        String saName = saClaim != null ? asString(saClaim.get("name")) : null;
        String saUid = saClaim != null ? asString(saClaim.get("uid")) : null;
        if (claimNamespace == null || podName == null || podUid == null || saName == null || saUid == null) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "kubernetes.io claims missing");
        }
        if (!AuthUtils.WORKER_SA_NAME.equals(saName) || !namespace.equals(claimNamespace)) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "kubernetes.io claims do not match sub");
        }

        Optional<WorkerIdentifierRecord> record =
                workerIdentifierService.findWorkerIdentifiersBySaUid(clusterId, saUid);
        if (record.isEmpty()) {
            log.debug("No worker identifiers registered for cluster={} saUid", clusterId);
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "no worker identifiers registered");
        }
        WorkerIdentifierRecord reg = record.get();
        String instanceId = reg.getKey().getInstanceId();

        boolean identityMatches = sub.equals(reg.getSub())
                && namespace.equals(reg.getNamespace())
                && saUid.equals(reg.getSaUid());
        List<WorkerIdentifierUdt> identifiers = reg.getIdentifiers();
        boolean podMatches = identifiers != null && identifiers.stream()
                .anyMatch(wi -> podName.equals(wi.getName()) && podUid.equals(wi.getUid()));
        if (!identityMatches || !podMatches) {
            log.debug("Worker identity mismatch for cluster={} instance={}", clusterId, instanceId);
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "worker identity not in registered set");
        }

        return resolveBinding(jwt, clusterId, instanceId, podName, TOKEN_TYPE_PSAT);
    }

    private Outcome verifySpiffe(Jwt jwt, String clusterId, String sub) {
        Matcher m = SPIFFE_INSTANCE_WORKER.matcher(sub);
        if (!m.find()) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID,
                    "SPIFFE ID does not contain /instance/{id}/worker/{wid}");
        }
        String instanceId = m.group(1);
        String workerId = m.group(2);
        if (!sub.contains("/cluster/" + clusterId + "/")) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "SPIFFE ID cluster does not match audience");
        }

        Optional<WorkerIdentifierRecord> record =
                workerIdentifierService.findWorkerIdentifiers(clusterId, instanceId);
        if (record.isEmpty()) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "no worker identifiers registered");
        }
        List<WorkerIdentifierUdt> identifiers = record.get().getIdentifiers();
        boolean matched = identifiers != null && identifiers.stream()
                .anyMatch(wi -> sub.equals(wi.getName()));
        if (!matched) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "worker identity not in registered set");
        }

        return resolveBinding(jwt, clusterId, instanceId, workerId, TOKEN_TYPE_SPIFFE);
    }

    /**
     * The instance must exist, be placed on the token's cluster, and belong to a request; the
     * request supplies the workload binding. Any gap rejects the token.
     */
    private Outcome resolveBinding(Jwt jwt, String clusterId, String instanceId,
            String workerId, String tokenType) {
        Optional<InstanceV2Entity> instance = instanceV2Repository.findInstanceById(instanceId);
        if (instance.isEmpty() || instance.get().getRequestId() == null) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "registered instance not found");
        }
        if (!clusterId.equals(instance.get().getZone())) {
            log.warn("Worker identity for instance={} registered by cluster={} but instance is on zone={}",
                    instanceId, clusterId, instance.get().getZone());
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "instance not placed on token cluster");
        }
        Optional<InstanceRequestV2Entity> request =
                instanceRequestV2Repository.findRequestById(instance.get().getRequestId());
        if (request.isEmpty()) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "request for instance not found");
        }
        InstanceRequestV2Entity req = request.get();
        if (req.getFunctionId() == null && req.getTaskId() == null) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "request carries no workload binding");
        }
        if (jwt.getExpiresAt() == null) {
            return Outcome.reject(RejectReason.SIGNATURE_INVALID, "token has no expiry");
        }

        return Outcome.builder()
                .jwt(jwt)
                .clusterId(clusterId)
                .instanceId(instanceId)
                .workerId(workerId)
                .tokenType(tokenType)
                .audience(firstAudience(jwt))
                .exp(jwt.getExpiresAt().getEpochSecond())
                .requestId(req.getRequestId())
                .functionId(uuidToString(req.getFunctionId()))
                .functionVersionId(uuidToString(req.getFunctionVersionId()))
                .taskId(uuidToString(req.getTaskId()))
                .ncaId(req.getNcaId())
                .build();
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> asMap(Object o) {
        return o instanceof Map<?, ?> m ? (Map<String, Object>) m : null;
    }

    private static String asString(Object o) {
        return o instanceof String s && !s.isBlank() ? s : null;
    }

    private static String uuidToString(UUID id) {
        return id != null ? id.toString() : null;
    }

    private static String firstAudience(Jwt jwt) {
        List<String> audiences = jwt.getAudience();
        return (audiences != null && !audiences.isEmpty()) ? audiences.get(0) : null;
    }

    /** Exactly one of {@code jwt} (active) or {@code reason} (rejected) is set. */
    @Getter
    @Builder
    public static final class Outcome {
        private final Jwt jwt;
        private final String clusterId;
        private final String instanceId;
        private final String workerId;
        private final String tokenType;
        private final String audience;
        private final Long exp;
        private final String requestId;
        private final String functionId;
        private final String functionVersionId;
        private final String taskId;
        private final String ncaId;
        private final RejectReason reason;
        private final String errorMessage;

        public static Outcome reject(RejectReason reason, String message) {
            return Outcome.builder().reason(reason).errorMessage(message).build();
        }

        public boolean isActive() {
            return jwt != null;
        }
    }
}
