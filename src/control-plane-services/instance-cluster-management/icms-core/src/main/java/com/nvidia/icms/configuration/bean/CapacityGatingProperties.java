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
package com.nvidia.icms.configuration.bean;

import jakarta.annotation.Nullable;
import jakarta.annotation.PostConstruct;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.event.EventListener;

import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * Capacity gating based on ncaId, clusters and gpus
 *
 * <p>Binds the same {@code icms} prefix as {@link IcmsConfigurationProperties}. Spring binds each
 * bean against the prefix independently, so the two keys stay at {@code icms.gpu-allowed-nca-ids}
 * and {@code icms.capacity-gating} and no deployed YAML or ConfigMap changes.
 *
 * <p>The two keys are separate layers that AND together: a GPU grant here is still subject to the
 * account's capacity policy, and vice versa. They live together because
 * {@link #logGrantsWithoutPolicy()} has to read both to catch the combination that denies an
 * account while neither block looks wrong on its own.
 */
@RefreshScope
@Configuration
@ConfigurationProperties("icms")
@Data
@Slf4j
public class CapacityGatingProperties {

    private static final String WILDCARD = "*";

    // Maps a GPU type to the NCA IDs allowed to see/allocate it. A GPU absent from the map is
    // unrestricted; the wildcard "*" allows everyone. Used to gate limited compute-platform
    // capacity to dedicated orgs. Empty by default = existing behavior.
    private Map<String, List<String>> gpuAllowedNcaIds = new HashMap<>();

    // Per-NCA capacity policy: ncaId -> clusters + GPUs/instance types. An ncaId absent from this
    // map is unrestricted by capacity gating: every cluster, GPU and instance type is allowed and
    // the filtering passes are skipped entirely. Only accounts named here are ever constrained,
    // which is why an empty map reproduces pre-gating behavior exactly. Note this says nothing
    // about icms.gpu-allowed-nca-ids, which gates GPUs independently and can still deny an account
    // that has no entry here. Compiled into capacityPolicies and denyAllNcaIds, which are what the
    // accessors read; never read this field directly.
    private Map<String, GatingPolicy> capacityGating = new HashMap<>();

    /**
     * Superseded by {@link #capacityGating}.
     */
    @Deprecated(forRemoval = true)
    private Map<String, Map<String, List<String>>> gpuGating = new HashMap<>();

    private volatile Map<String, GatingPolicy> capacityPolicies = Map.of();

    // NCA IDs whose entry is unusable (no recognized field) or explicitly disabled
    // (clusters: []). Both deny everything for that account and nothing for any other.
    private volatile Set<String> denyAllNcaIds = Set.of();

    private static final String MESG_CAPACITY_GATING_UNUSABLE =
            "icms.capacity-gating lists ncaId {} with neither a clusters nor a gpus key, so the "
                    + "intent cannot be determined; denying all capacity for this account until "
                    + "the entry is fixed";

    private static final String MESG_CAPACITY_GATING_DISABLED =
            "icms.capacity-gating sets clusters to an empty list for ncaId {}, disabling the "
                    + "account; it will be denied all capacity including clusters it owns";

    private static final String MESG_CAPACITY_GATING_NO_INSTANCE_TYPES =
            "icms.capacity-gating lists GPU {} for ncaId {} with no instance types, denying the "
                    + "GPU for this account";

    private static final String MESG_CAPACITY_GATING_BLANK_GPU_NAME =
            "icms.capacity-gating lists an entry with a null or blank GPU name for ncaId {}, "
                    + "dropping the entry";

    private static final String MESG_CAPACITY_GATING_BLANK_CLUSTER_ID =
            "icms.capacity-gating lists a null or blank cluster id for ncaId {}, the entry can "
                    + "never match a cluster";

    private static final String MESG_GRANT_NOT_IN_POLICY =
            "icms.gpu-allowed-nca-ids grants GPU {} to ncaId {}, but that account's "
                    + "icms.capacity-gating gpus map omits it, so the grant has no effect and the "
                    + "GPU stays denied; add {} to the account's gpus map or drop the grant";

    private static final String MESG_LEGACY_GPU_GATING =
            "icms.gpu-gating is set for {} ncaId(s) {} but has been replaced by "
                    + "icms.capacity-gating and is ignored; migrate the entries and remove the key";

    @PostConstruct
    public void onStartup() {
        compileCapacityGating();
    }

    @EventListener(RefreshScopeRefreshedEvent.class)
    public void onRefresh() {
        compileCapacityGating();
    }

    /**
     * Whether the given NCA ID may see/allocate the given GPU, per {@code icms.gpu-allowed-nca-ids}.
     * A GPU absent from the map (or with an empty list) is unrestricted; a list containing the
     * wildcard {@code "*"} allows everyone. Otherwise only the listed NCA IDs are permitted.
     *
     * @param gpu   the GPU type
     * @param ncaId the requesting NGC org / NCA ID
     * @return {@code true} if allowed (including the unrestricted default), {@code false} otherwise
     */
    public boolean isNcaAllowedForGpu(String gpu, String ncaId) {
        List<String> allowed = gpuAllowedNcaIds.get(gpu);
        if (allowed == null || allowed.isEmpty()) {
            return true;
        }
        return allowed.contains(WILDCARD) || allowed.contains(ncaId);
    }

    /**
     * Whether {@code icms.capacity-gating} constrains this NCA ID at all. False means the
     * account is ungated and every filtering pass can be skipped outright.
     */
    public boolean hasCapacityGating(@Nullable String ncaId) {
        return ncaId != null
                && (capacityPolicies.containsKey(ncaId) || denyAllNcaIds.contains(ncaId));
    }

    /**
     * Whether the account is denied all capacity, either because {@code clusters} is an empty
     * list (the deliberate kill switch) or because its entry could not be understood.
     */
    public boolean isOrgDenied(@Nullable String ncaId) {
        return ncaId != null && denyAllNcaIds.contains(ncaId);
    }

    /**
     * Whether this NCA ID has a cluster allow list, as opposed to being restricted only by GPU.
     * Lets callers skip the nested walk down to cluster level for accounts whose policy never
     * consults cluster identity.
     */
    public boolean hasClusterRestriction(@Nullable String ncaId) {
        GatingPolicy policy = policyFor(ncaId);
        return policy != null && policy.getClusters() != null;
    }

    /**
     * Whether {@code icms.capacity-gating} lets the NCA ID see and allocate in the given cluster.
     * For compute-platform rows the cluster id is the zone name, so this is also the zone gate.
     *
     * @param ncaId     the NGC org / NCA ID
     * @param clusterId the cluster id, which is the zone name for compute-platform clusters
     * @return {@code true} if allowed, including when the NCA ID has no gating entry
     */
    public boolean isClusterAllowedForNca(@Nullable String ncaId, @Nullable String clusterId) {
        if (isOrgDenied(ncaId)) {
            return false;
        }
        GatingPolicy policy = policyFor(ncaId);
        if (policy == null || policy.getClusters() == null) {
            return true;
        }
        if (clusterId == null || clusterId.isBlank()) {
            return false;
        }
        return policy.getClusters().contains(WILDCARD)
                || policy.getClusters().contains(clusterId);
    }

    /**
     * Whether {@code icms.capacity-gating} lets the NCA ID see and allocate the given GPU.
     *
     * @param ncaId   the NGC org / NCA ID
     * @param gpuName the GPU type
     * @return {@code true} if allowed, including when the NCA ID has no gating entry
     */
    public boolean isGpuAllowedForNca(@Nullable String ncaId, @Nullable String gpuName) {
        if (isOrgDenied(ncaId)) {
            return false;
        }
        Map<String, List<String>> allowedByGpu = allowedGpusFor(ncaId);
        if (allowedByGpu == null) {
            return true;
        }
        if (gpuName == null || gpuName.isBlank()) {
            return false;
        }
        // A GPU configured without usable instance types denies that GPU. Reading it as
        // "all instance types" would let a truncated config silently widen access.
        return hasNonBlank(allowedByGpu.get(gpuName));
    }

    /**
     * Whether {@code icms.capacity-gating} lets the NCA ID see and allocate the given instance
     * type. An instance type on a GPU that is itself gated out is never allowed.
     *
     * @param ncaId        the NGC org / NCA ID
     * @param gpuName      the GPU type the instance type belongs to
     * @param instanceType the instance type name
     * @return {@code true} if allowed, including when the NCA ID has no gating entry
     */
    public boolean isInstanceTypeAllowedForNca(@Nullable String ncaId, @Nullable String gpuName,
                                               @Nullable String instanceType) {
        if (isOrgDenied(ncaId)) {
            return false;
        }
        Map<String, List<String>> allowedByGpu = allowedGpusFor(ncaId);
        if (allowedByGpu == null) {
            return true;
        }
        if (gpuName == null || gpuName.isBlank() || instanceType == null
                || instanceType.isBlank()) {
            return false;
        }
        List<String> allowedInstanceTypes = allowedByGpu.get(gpuName);
        if (allowedInstanceTypes == null) {
            return false;
        }
        return allowedInstanceTypes.contains(WILDCARD)
                || allowedInstanceTypes.contains(instanceType);
    }

    /**
     * The GPU allow list for this NCA ID, or {@code null} when every GPU is permitted. A policy
     * whose {@code gpus} key is absent or empty places no GPU restriction: the account asked for
     * nothing in particular, so only its cluster rules apply.
     */
    @Nullable
    private Map<String, List<String>> allowedGpusFor(@Nullable String ncaId) {
        GatingPolicy policy = policyFor(ncaId);
        if (policy == null) {
            return null;
        }
        Map<String, List<String>> allowedByGpu = policy.getGpus();
        return allowedByGpu == null || allowedByGpu.isEmpty() ? null : allowedByGpu;
    }

    @Nullable
    private GatingPolicy policyFor(@Nullable String ncaId) {
        return ncaId == null ? null : capacityPolicies.get(ncaId);
    }

    /**
     * Keeps the compiled view in step with the raw map however it is replaced, whether by the
     * config binder, a refresh, or a test. Reading the raw map in the accessors instead would
     * mean re-validating on every call.
     */
    public void setCapacityGating(@Nullable Map<String, GatingPolicy> capacityGating) {
        this.capacityGating = capacityGating;
        rebuildCapacityPolicies();
    }

    /**
     * Rebuilds {@link #capacityPolicies} and {@link #denyAllNcaIds} and reports anything an
     * operator needs to fix. Runs at startup and on every refresh.
     *
     * <p>Never throws. A refresh that propagates an exception leaves the bean on its previous
     * config with no signal that the new config was rejected, which is silent drift; a bad entry
     * is confined to its own account instead.
     */
    private void compileCapacityGating() {
        rebuildCapacityPolicies();
        logCapacityGatingIssues();
        logGrantsWithoutPolicy();
        logLegacyGatingKeys();
    }

    /**
     * Partitions the configured entries into usable policies and denied accounts. Silent, so
     * that the binder calling the setter repeatedly does not multiply the startup diagnostics.
     */
    private void rebuildCapacityPolicies() {
        Map<String, GatingPolicy> policies = new HashMap<>();
        Set<String> deniedNcaIds = new HashSet<>();

        if (capacityGating != null) {
            capacityGating.forEach((ncaId, policy) -> {
                if (ncaId == null || ncaId.isBlank()) {
                    return;
                }
                // An entry with neither key cannot be read as an allow list or as a kill switch.
                // Guessing either way risks silently widening access, so the account is denied
                // until an operator resolves it. An empty cluster list is the explicit switch.
                //
                // Reached by an NCA ID written with nothing under it, which binds to a policy
                // with both fields unset. It is not reached by an entry whose keys are all
                // unrecognized, such as the old gpu-gating shape copied under this key: unknown
                // properties are ignored, so the binder yields no entry and the account is left
                // ungated and undiagnosed. See CapacityGatingBindingTest for both.
                if (policy == null || (policy.getClusters() == null && policy.getGpus() == null)
                        || (policy.getClusters() != null && policy.getClusters().isEmpty())) {
                    deniedNcaIds.add(ncaId);
                    return;
                }
                policies.put(ncaId, policy);
            });
        }

        this.capacityPolicies = Map.copyOf(policies);
        this.denyAllNcaIds = Set.copyOf(deniedNcaIds);
    }

    private void logCapacityGatingIssues() {
        if (capacityGating == null) {
            return;
        }
        capacityGating.forEach((ncaId, policy) -> {
            if (ncaId == null || ncaId.isBlank()) {
                return;
            }
            if (policy == null || (policy.getClusters() == null && policy.getGpus() == null)) {
                log.error(MESG_CAPACITY_GATING_UNUSABLE, ncaId);
                return;
            }
            if (policy.getClusters() != null && policy.getClusters().isEmpty()) {
                log.warn(MESG_CAPACITY_GATING_DISABLED, ncaId);
                return;
            }
            if (policy.getClusters() != null
                    && policy.getClusters().stream().anyMatch(id -> id == null || id.isBlank())) {
                log.error(MESG_CAPACITY_GATING_BLANK_CLUSTER_ID, ncaId);
            }
            if (policy.getGpus() == null) {
                return;
            }
            policy.getGpus().forEach((gpuName, instanceTypes) -> {
                if (gpuName == null || gpuName.isBlank()) {
                    log.error(MESG_CAPACITY_GATING_BLANK_GPU_NAME, ncaId);
                } else if (!hasNonBlank(instanceTypes)) {
                    log.error(MESG_CAPACITY_GATING_NO_INSTANCE_TYPES, gpuName, ncaId);
                }
            });
        });
    }

    /**
     * Flags a grant in {@code icms.gpu-allowed-nca-ids} that its own account's capacity policy
     * then withholds. The two keys are separate layers that AND together, so an operator can
     * grant a restricted GPU and still see it denied, with nothing in either block looking wrong
     * on its own.
     */
    private void logGrantsWithoutPolicy() {
        if (gpuAllowedNcaIds == null) {
            return;
        }
        gpuAllowedNcaIds.forEach((gpu, grantedNcaIds) -> {
            if (gpu == null || grantedNcaIds == null || grantedNcaIds.contains(WILDCARD)) {
                return;
            }
            for (String ncaId : grantedNcaIds) {
                Map<String, List<String>> allowedByGpu = allowedGpusFor(ncaId);
                if (allowedByGpu != null && !allowedByGpu.containsKey(gpu)) {
                    log.error(MESG_GRANT_NOT_IN_POLICY, gpu, ncaId, gpu);
                }
            }
        });
    }

    /**
     * Reports environments still shipping the replaced key. Warning only: the packaged profile
     * may already carry the converted policy while a ConfigMap still carries the old key, so
     * acting on the leftover would deny an account the new config correctly permits.
     */
    private void logLegacyGatingKeys() {
        if (gpuGating != null && !gpuGating.isEmpty()) {
            log.error(MESG_LEGACY_GPU_GATING, gpuGating.size(), gpuGating.keySet());
        }
    }

    private static boolean hasNonBlank(@Nullable List<String> values) {
        if (values == null) {
            return false;
        }
        for (String value : values) {
            if (value != null && !value.isBlank()) {
                return true;
            }
        }
        return false;
    }

    /**
     * One account's entry under {@code icms.capacity-gating}. Both fields are left null when the
     * key is absent, and absence is meaningful, so neither may be given a default value.
     */
    @Data
    @NoArgsConstructor
    @AllArgsConstructor
    @Builder
    public static class GatingPolicy {

        /**
         * Clusters this account may use, and the whole answer to where it may run: a cluster the
         * account registered itself is filtered on the same terms as any other, so it has to be
         * named here to be usable. Absent means every cluster, as does the wildcard {@code "*"}.
         * An empty list is the account kill switch and denies everything, owned clusters
         * included.
         */
        private List<String> clusters;

        /**
         * GPUs this account may use, mapped to the instance types allowed within each. Absent or
         * empty means every GPU and every instance type. When present, a GPU missing from the map
         * is denied. Within a GPU, {@code "*"} allows every instance type and an empty list
         * denies the GPU outright, so a truncated config cannot silently widen access.
         */
        private Map<String, List<String>> gpus;
    }
}
