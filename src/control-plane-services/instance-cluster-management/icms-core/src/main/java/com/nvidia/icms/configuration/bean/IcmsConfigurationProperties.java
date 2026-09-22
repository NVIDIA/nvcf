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

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.outbound.sqs.QueueManager;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.annotation.PostConstruct;
import jakarta.validation.constraints.NotNull;
import lombok.AccessLevel;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.EqualsAndHashCode;
import lombok.Getter;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
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

@RefreshScope
@Configuration
@ConfigurationProperties("icms")
@Data
@Slf4j
public class IcmsConfigurationProperties {

    @Autowired
    private AwsConfigurationProperties awsConfigurationProperties;

    @Autowired
    private AwsQueueProperties awsQueueProperties;

    @Autowired
    private QueueManager queueManager;

    private boolean queuePerInstanceEnabled;
    private boolean queueCreationPerInstanceTypeEnabled;
    private Integer instanceBatchCount;
    private Integer reservedInstanceBatchCount;

    private Integer requestCancelDurationInMin;

    private Integer instanceLifetimeValidityInDays;

    private Integer terminateExpiredRequestFromPastMonths;

    private boolean terminateExpiredInstancesEnabled;

    private boolean cloudFailureDetectionEnabled;

    private Integer ttlToMarkCloudUnhealthy;

    private boolean checkForDuplicateInstances;

    private boolean populateAcknowledgedInstances;

    private boolean instanceListingApiEnabled;

    private boolean stateRequestDeletionTaskEnabled;
    private int closedRequestInstanceDeletionDays;
    private int staleRequestsTelemetryEventSize;
    private int waitForInstancesToBeCreatedInDays;

    private int staleRequestRecordsInDbPage;
    private int staleRequestPauseBetweenPagesInMs;
    private int staleRequestPauseBetweenRecordsInMs;

    private int cloudFailureDetectionTaskLockTtlInSeconds;
    private int cancelLingeringRequestsTaskLockTtlInSeconds;
    private int clusterHealthMonitorTaskLockTtlInSeconds;
    private int staleRequestDeletionTaskLockTtlInSeconds;
    private int terminateExpiredInstancesTaskLockTtlInSeconds;
    private int waitForDbLockByTtlValidationInSeconds;
    private int gpuUsageTaskLockTtlInSeconds;
    private int gpuUsageTaskBoundSleepDurationInSeconds;

    private int cloudFailureDetectionTaskPauseBetweenDaysInMilliseconds;

    private int staleRequestDeletionStartUtcHour24H;
    private int staleRequestDeletionEndUtcHour24H;

    private boolean messageBatchIdExpiryValidationInGet;
    private int cancelRequestUpToPastMonths;
    private MessageBatchIdConfig messageBatchIdConfig;

    private int dbQueryExecutorMaxThreads;
    private int sqsBatchSize;

    private int sqsBatchMaxSizeInBytes;

    private boolean databaseCleanupTaskEnabled;
    private int databaseRecordsTtlInDays;
    private int databaseCleanupLockTtlInSeconds;
    private int databaseCleanupLookupPeriodInDays;
    private boolean databaseCleanupRequestsEnabled;
    private boolean databaseCleanupInstancesEnabled;
    private int databaseCleanupDbPageSize;

    private int findInstanceByRequestIdCallsPerThread;
    private int findInstanceByRequestIdThreadsInParallel;

    private boolean findInstancesByRequestIdForGpuUsageInParallel;

    private int gpusV5PopulationTaskLockTtlInSeconds;
    private boolean gpuV5PopulationTaskEnabled;

    private int databaseReadPageSize;

    private boolean shuttingDownInstanceTerminationTaskEnabled;
    private int shuttingDownInstanceTerminationTaskLockTtlInSeconds;
    private int shuttingDownInstanceTerminationThresholdInHours;

    private boolean clusterGroupInstanceTypeUsageFilteringEnabled;

    private boolean fndsMessagesEnabled;
    private boolean fndsMessagesV1Enabled;
    private boolean fndsMessagesV2Enabled;
    private boolean fndsMessagesV3Enabled;

    private boolean airGappedModeEnabled;

    private long wildCardStaleCachedDataValidDurationInSec;

    private boolean gpuUsagePerInstanceTaskEnabled;

    // Feature flag to enable request state transition from OPEN to ACTIVE when first instance is created
    private boolean requestStateTransitionToActiveEnabled;

    private boolean gpuUsageTaskSecureRandomEnabled;

    private boolean includeCustomPublicClustersInAccountInfoApis;

    private boolean reservationBackupEnabled;

    // Feature flag: serve cluster and cluster-group reads from cluster_by_cluster_id using
    // storage-attached indexes (SAI) instead of the legacy materialized tables
    // (clusters_by_account, clusters_by_authorized_accounts, cluster_by_group_id_and_cluster_id,
    // cluster_group_by_cluster_group_id, cluster_groups_by_account,
    // cluster_groups_by_authorized_accounts). Writes are unaffected.
    private boolean clusterByIdReadsEnabled;

    private Map<String, List<String>> supportedGpuDetails = new HashMap<>();
    private Map<String, String> gpusToQueueUrlGpuNameMap = new HashMap<>();

    // Maps a GPU type to the NCA IDs allowed to see/allocate it. A GPU absent from the map is
    // unrestricted; the wildcard "*" allows everyone. Used to gate limited compute-platform
    // capacity to dedicated orgs. Empty by default = existing behavior.
    private Map<String, List<String>> gpuAllowedNcaIds = new HashMap<>();

    // Per-NCA allowlist of GPUs and their instance types: ncaId -> gpuName -> instanceTypes.
    // Empty by default = existing behavior. See getSanitizedGpuGating() for malformed entries.
    private Map<String, Map<String, List<String>>> gpuGating = new HashMap<>();

    // Lazily built, validated view of gpuGating: ncaId -> gpuName -> instanceTypes.
    // Rebuilt per bean instance, so @RefreshScope gives a fresh view after each remote refresh.
    @Getter(AccessLevel.NONE)
    @Setter(AccessLevel.NONE)
    @EqualsAndHashCode.Exclude
    private volatile Map<String, Map<String, Set<String>>> sanitizedGpuGating;

    private Set<String> supportedInstanceTypes = new HashSet<>();
    private Set<String> supportedGpus = new HashSet<>();

    private static final String MESG_REMOTE_CONFIG_REFRESH =
            "Remote config refresh observed: icms.instance-batch-count = %s";

    private static final String MESG_GPU_GATING_NO_GPUS =
            "icms.gpu-gating lists ncaId {} with no GPUs, ignoring the entry and leaving the " +
                    "account ungated";

    private static final String MESG_GPU_GATING_NO_INSTANCE_TYPES =
            "icms.gpu-gating lists GPU {} for ncaId {} with no instance types, denying the GPU " +
                    "for this account";

    private static final String MESG_GPU_GATING_BLANK_GPU_NAME =
            "icms.gpu-gating lists an entry with a null or blank GPU name for ncaId {}, dropping " +
                    "the entry";

    // Temporary verification hook; remove after remote config support is complete.
    @EventListener(RefreshScopeRefreshedEvent.class)
    public void logRemoteConfigRefresh() {
        log.info(MESG_REMOTE_CONFIG_REFRESH.formatted(instanceBatchCount));
    }

    @PostConstruct
    public void setCustomValues() {

        if (sqsBatchSize > 10 || sqsBatchSize <= 0) {
            log.warn(
                    "Wrong sqsBatchSize range is 0<sqsBatchSize<=10, provided value: {}. Setting 10 as default value",
                    sqsBatchSize);
            sqsBatchSize = 10;
        }

        if (closedRequestInstanceDeletionDays < 2) {
            log.info(
                    "closedRequestInstanceDeletionDays is less that 2, setting 2 as default value");
            closedRequestInstanceDeletionDays = 2;
        }

        this.supportedGpuDetails.values().forEach(instanceTypes ->
                                                          supportedInstanceTypes.addAll(
                                                                  instanceTypes));

        this.supportedGpus.addAll(this.supportedGpuDetails.keySet());

        createSqsQueues();

        if (messageBatchIdConfig == null) {
            log.info("messageBatchIdConfig is not provided setting null value");
            messageBatchIdConfig = MessageBatchIdConfig.builder().build();
        }
    }

    private void createSqsQueues() {
        if (!queueCreationPerInstanceTypeEnabled) {
            log.debug("Queue creation per instance type feature is not enabled");
            return;
        }
        log.debug("Queue creation per instance type feature is enabled...creating queues");
        for (String gpuName : supportedGpus) {

            String gpuNameForQueues = getGpuNameForQueues(gpuName);
            if (gpuNameForQueues == null) {
                log.error("Failed to find gpuNameForQueue for {} gpu", gpuName);
                continue;
            }
            createSqsQueues(gpuNameForQueues,
                            awsConfigurationProperties.getQueuePerInstanceNameFormat(),
                            awsQueueProperties.getQueueAttributes());

            createSqsQueues(gpuNameForQueues,
                            awsConfigurationProperties.getQueuePerInstanceNameFormatForTasks(),
                            awsQueueProperties.getTasksQueueAttributes());
        }
    }

    private void createSqsQueues(String gpuNameForQueues, String queueNameFormat, Map<String, String> queueAttributes) {
        String queueName =
                String.format(queueNameFormat, gpuNameForQueues).toLowerCase();
        if (queueManager.queueExists(queueName)) {
            log.debug("Queue with name {} already exists, skip attempt to create.", queueName);
            if (queueManager.isQueueAttributesUpdateNeeded(
                    queueManager.getQueueUrl(queueName, false),
                    queueAttributes)) {
                queueManager.updateQueueAttributes(queueManager.getQueueUrl(queueName, false),
                                                   queueAttributes);
            }
        } else {
            try {
                log.debug("Creating queue with name {}", queueName);
                queueManager.createQueue(queueName, queueAttributes);
            } catch (Exception e) {
                log.error("Failed to create queue with name {}, error: ", queueName, e);
            }
        }
    }

    @Observed
    public @Nullable String getCreationQueueUrlForGpu(
            @NotNull String gpuName, boolean isRequestForTask) {

        String gpuNameForQueue = getGpuNameForQueues(gpuName);
        if (gpuNameForQueue == null) {
            return null;
        }

        String queueName;
        if (isRequestForTask) {
            queueName = String.format(
                    awsConfigurationProperties.getQueuePerInstanceNameFormatForTasks(),
                    gpuNameForQueue);
        } else {
            queueName =
                    String.format(awsConfigurationProperties.getQueuePerInstanceNameFormat(),
                                  gpuNameForQueue);
        }

        return queueManager.getQueueUrl(queueName.toLowerCase(), true);
    }

    public boolean isInstanceTypeSupported(String instanceType) {
        return supportedInstanceTypes.contains(instanceType);
    }

    public boolean isGpuSupported(String gpuName) {
        return supportedGpus.contains(gpuName);
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
        return allowed.contains("*") || allowed.contains(ncaId);
    }

    public boolean hasGpuGating(@Nullable String ncaId) {
        return allowedGpusFor(ncaId) != null;
    }

    /**
     * Whether {@code icms.gpu-gating} lets the NCA ID see and allocate the given GPU.
     *
     * @param ncaId   the NGC org / NCA ID
     * @param gpuName the GPU type
     * @return {@code true} if allowed, including when the NCA ID has no gating entry
     */
    public boolean isGpuAllowedForNca(@Nullable String ncaId, @Nullable String gpuName) {
        Map<String, Set<String>> allowedByGpu = allowedGpusFor(ncaId);
        return allowedByGpu == null || allowedByGpu.containsKey(gpuName);
    }

    /**
     * Whether {@code icms.gpu-gating} lets the NCA ID see and allocate the given instance type.
     * An instance type on a GPU that is itself gated out is never allowed.
     *
     * @param ncaId        the NGC org / NCA ID
     * @param gpuName      the GPU type the instance type belongs to
     * @param instanceType the instance type name
     * @return {@code true} if allowed, including when the NCA ID has no gating entry
     */
    public boolean isInstanceTypeAllowedForNca(@Nullable String ncaId, @Nullable String gpuName,
                                               @Nullable String instanceType) {
        Map<String, Set<String>> allowedByGpu = allowedGpusFor(ncaId);
        if (allowedByGpu == null) {
            return true;
        }
        Set<String> allowedInstanceTypes = allowedByGpu.get(gpuName);
        return allowedInstanceTypes != null && allowedInstanceTypes.contains(instanceType);
    }

    @Nullable
    private Map<String, Set<String>> allowedGpusFor(@Nullable String ncaId) {
        if (ncaId == null) {
            return null;
        }
        return getSanitizedGpuGating().get(ncaId);
    }

    /**
     * Resets the cached gating view so the next read re-reads {@link #gpuGating}. Declared
     * explicitly so Lombok does not generate a setter that would leave the cache stale.
     */
    public void setGpuGating(@Nullable Map<String, Map<String, List<String>>> gpuGating) {
        this.gpuGating = gpuGating;
        this.sanitizedGpuGating = null;
    }

    /**
     * Validated view of {@code icms.gpu-gating}, built once per bean instance. Validation lives
     * here so a malformed remote config can never fail startup or a {@code @RefreshScope} rebind; 
     * a bad entry is logged and dropped instead.
     *
     * <p>Malformed entries are handled asymmetrically on purpose:
     * <ul>
     *   <li>An NCA ID with no GPUs configured at all is treated as ungated</li>
     *   <li>A GPU with no instance types is dropped, which denies that GPU. Reading it as
     *       "all instance types" would let a truncated config silently widen access, so the
     *       blast radius is kept to one GPU instead.</li>
     * </ul>
     */
    private Map<String, Map<String, Set<String>>> getSanitizedGpuGating() {
        Map<String, Map<String, Set<String>>> sanitized = sanitizedGpuGating;
        if (sanitized == null) {
            synchronized (this) {
                sanitized = sanitizedGpuGating;
                if (sanitized == null) {
                    sanitized = sanitizeGpuGating();
                    sanitizedGpuGating = sanitized;
                }
            }
        }
        return sanitized;
    }

    private Map<String, Map<String, Set<String>>> sanitizeGpuGating() {
        if (gpuGating == null || gpuGating.isEmpty()) {
            return Map.of();
        }

        Map<String, Map<String, Set<String>>> sanitized = new HashMap<>();
        for (Map.Entry<String, Map<String, List<String>>> ncaEntry : gpuGating.entrySet()) {
            String ncaId = ncaEntry.getKey();
            Map<String, List<String>> configuredGpus = ncaEntry.getValue();

            if (configuredGpus == null || configuredGpus.isEmpty()) {
                log.error(MESG_GPU_GATING_NO_GPUS, ncaId);
                continue;
            }

            Map<String, Set<String>> allowedByGpu = new HashMap<>();
            for (Map.Entry<String, List<String>> gpuEntry : configuredGpus.entrySet()) {
                String gpuName = gpuEntry.getKey();
                // A null key would make isGpuAllowedForNca allow a destination with no GPU name.
                if (gpuName == null || gpuName.isBlank()) {
                    log.error(MESG_GPU_GATING_BLANK_GPU_NAME, ncaId);
                    continue;
                }

                Set<String> instanceTypes = toNonBlankSet(gpuEntry.getValue());
                if (instanceTypes.isEmpty()) {
                    log.error(MESG_GPU_GATING_NO_INSTANCE_TYPES, gpuName, ncaId);
                    continue;
                }
                allowedByGpu.put(gpuName, instanceTypes);
            }

            // Kept even when every GPU was dropped: the org asked to be gated, so denying its
            // malformed GPUs is safer than silently reverting it to unrestricted access.
            sanitized.put(ncaId, allowedByGpu);
        }
        return sanitized;
    }

    private static Set<String> toNonBlankSet(@Nullable List<String> values) {
        if (values == null || values.isEmpty()) {
            return Set.of();
        }
        Set<String> result = new HashSet<>();
        for (String value : values) {
            if (value != null && !value.isBlank()) {
                result.add(value);
            }
        }
        return result;
    }

    /**
     * GPU name used in the global queues differs from the GPU name provided at the time
     * of cluster registration. Use this to translate registeredGpuName -> queueGpuName.
     */
    @Observed
    @VisibleForTesting
    public String getGpuNameForQueues(@NotNull String gpuName) {
        String gpuNameForQueue = this.gpusToQueueUrlGpuNameMap.get(gpuName);
        if (gpuNameForQueue == null) {
            // TODO: Setup an alert based on this warn message
            log.warn("Cannot find mapping of gpuName to gpuNameForQueue, gpuName {}",
                     gpuName);
        }
        return gpuNameForQueue;
    }

    /** Subset of the {@code icms.message-batch-id-config} block. */
    @Data
    @AllArgsConstructor
    @Builder
    public static class MessageBatchIdConfig {

        private int validationDurationForByocWithModelInMin;
        private int validationDurationForByocWithoutModelInMin;
        private boolean cancelRequestValidationEnabled;
        private int validationDurationInMin;
    }
}
