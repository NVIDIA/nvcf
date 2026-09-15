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
package com.nvidia.icms.service.scheduled.gpuusage;

import static com.nvidia.icms.scheduled.GpuUsageTaskController.GPU_USAGE_EVENT_NAME;
import static com.nvidia.icms.service.InstanceServiceHelper.getCloudProvider;
import static com.nvidia.icms.service.telemetry.model.Events.GPU_USAGE_PER_INSTANCE_EVENT_FAILED;
import static com.nvidia.icms.service.telemetry.model.Events.RUNNING_DURATION_FINDING_FOR_GPU_USAGE_FAILED;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValueOfUuid;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.bean.InstanceTypeConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel.LaunchSpecification;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.LatestInstanceStateEventService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.scheduled.gpuusage.GpuUsageEventService.InstanceLaunchSpecificationInfo.InstanceLaunchSpecificationInfoBuilder;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.jetbrains.annotations.NotNull;
import org.jetbrains.annotations.Nullable;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class GpuUsageEventService {

    @Data
    @AllArgsConstructor
    @Builder
    private static class InstanceInfoForGpuUsage {

        Instant currentJobExecutionTime;
        Instant terminationTime;
        Instant previousJobExecutionTime;
        Instant creationTime;
        double instanceRunningDuration;
        boolean isInstanceCreatedBeforePreviousJob;
    }

    @AllArgsConstructor
    @NoArgsConstructor
    @Data
    @Builder
    static class InstanceLaunchSpecificationInfo {

        private String functionId;
        private String taskId;
        private String ncaId;
        private String gpu;
        private String instanceType;
        private String functionName;
        private String taskName;
        private String ncaIdAccountName;

        private String cloudProvider;
        private String reservationId;
        private String capacityType;
    }

    private final TelemetryEventClient telemetryEventClient;
    private final ClusterRepository clusterRepository;
    private final GpuUsageEventServiceHelper gpuUsageEventServiceHelper;
    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final InstanceTypeConfigurationProperties instanceTypeConfigurationProperties;
    private final LatestInstanceStateEventService latestInstanceStateEventService;
    private final ComputePlatformService computePlatformService;

    @Observed
    public void sendGpuUsageEventForRunningInstance(
            InstanceV2Entity instanceV2Entity,
            Instant currentJobExecutionTime,
            Instant previousJobExecutionTime,
            Map<String, ClusterProviderEnum> cloudProviderCache) {

        try {
            CloudProvider cloudProvider = findCloudProvider(cloudProviderCache,
                                                     instanceV2Entity);

            InstanceInfoForGpuUsage instanceInfoForGpuUsage = getDurationRunningInstance(
                    instanceV2Entity,
                    currentJobExecutionTime,
                    previousJobExecutionTime);

            int gpuCount = getGpuCountAssociatedToInstance(cloudProvider, instanceV2Entity);

            double gpuUsageInHours = instanceInfoForGpuUsage.getInstanceRunningDuration() * gpuCount;

            // Only sending positive GPU usage hours so that negative data should not pollute overall metrics
            if (gpuUsageInHours < 0) {
                sendInvalidGpuUsageEvent(instanceV2Entity, instanceInfoForGpuUsage, gpuCount);
                return;
            }

            sendGpuUsageEvent(instanceV2Entity, gpuUsageInHours, cloudProvider, gpuCount, instanceInfoForGpuUsage);
            latestInstanceStateEventService.sendLatestInstanceStateEvent(instanceV2Entity);

        } catch (Exception exception) {
            // Suppressing the error
            log.error(
                    "job: {} failed to send gpuUsage event for running instances, instanceId {}, error: {}",
                    GPU_USAGE_EVENT_NAME,
                    instanceV2Entity.getInstanceId(),
                    exception.getMessage());

            sendGpuUsageFindingFailedEvent(exception.getMessage(),
                                           instanceV2Entity.getInstanceId(),
                                           instanceV2Entity.getInstanceStateName());
        }
    }

    @Observed
    public void sendGpuUsageEventForTerminatedInstance(InstanceV2Entity instanceV2Entity) {

        try {
            InstanceInfoForGpuUsage instanceInfoForGpuUsage = getDurationOfTerminatedInstance(
                    instanceV2Entity);

            // For termination flow, we won't maintain cloudProviderCache map as it will be single event
            CloudProvider cloudProvider = findCloudProvider(new HashMap<>(), instanceV2Entity);

            int gpuCount = getGpuCountAssociatedToInstance(cloudProvider, instanceV2Entity);

            double gpuUsageInHours = instanceInfoForGpuUsage.getInstanceRunningDuration() * gpuCount;

            // Only sending positive GPU usage hours so that negative data should not pollute overall metrics
            if (gpuUsageInHours < 0) {
                sendInvalidGpuUsageEvent(instanceV2Entity, instanceInfoForGpuUsage, gpuCount);
                return;
            }

            sendGpuUsageEvent(instanceV2Entity, gpuUsageInHours, cloudProvider, gpuCount,
                              instanceInfoForGpuUsage);

        } catch (Exception exception) {
            // Suppressing the error
            log.error(
                    "Event: {} failed to send gpuUsage event for terminated instances, instanceId {}, error: {}, exception: ",
                    GPU_USAGE_EVENT_NAME,
                    instanceV2Entity.getInstanceId(),
                    exception.getMessage(),
                    exception);

            sendGpuUsageFindingFailedEvent(exception.getMessage(),
                                           instanceV2Entity.getInstanceId(),
                                           instanceV2Entity.getInstanceStateName());
        }
    }

    /**
     * Calculates the instance running duration in hours based on its creation time and job execution times.
     * <p>
     * The function handles two scenarios:
     * 1. If the instance was created before the previous job execution:
     * - running duration = time between previous job and current job execution
     * 2. If the instance was created after the previous job execution:
     * - running duration = time between instance creation and current job execution
     *
     * @param instanceV2Entity     The instance entity to calculate GPU usage for
     * @param currentJobExecutionTime  The current job execution timestamp
     * @param previousJobExecutionTime The previous job execution timestamp
     * @return double The GPU usage duration in hours with 2 decimal places
     */

    private InstanceInfoForGpuUsage getDurationRunningInstance(
            InstanceV2Entity instanceV2Entity,
            Instant currentJobExecutionTime,
            Instant previousJobExecutionTime) {

        Instant creationTime = TimeUtils.getInstantFromUuid(instanceV2Entity.getCreateTimeuuid());
        double instanceRunningDuration = 0;

        // The instance is started before previous job and still running, gpuUsage = currentJobExecutionTime - previousJobExecutionTime
        if (creationTime.isBefore(previousJobExecutionTime)) {
            instanceRunningDuration = getDurationInHours(previousJobExecutionTime, currentJobExecutionTime);
        } else {

            // The instance is started after previous job and still running, gpuUsage = currentJobExecutionTime- creationTime
            instanceRunningDuration = getDurationInHours(creationTime, currentJobExecutionTime);
        }

        return InstanceInfoForGpuUsage.builder()
                .instanceRunningDuration(instanceRunningDuration)
                .isInstanceCreatedBeforePreviousJob(creationTime.isBefore(previousJobExecutionTime))
                .currentJobExecutionTime(currentJobExecutionTime)
                .previousJobExecutionTime(previousJobExecutionTime)
                .creationTime(creationTime)
                .terminationTime(null)
                .build();
    }

    /**
     * Calculates the instance running duration in hours for a terminated instance based on its creation and termination times.
     * <p>
     * The function handles two scenarios:
     * 1. If the instance was created before the previous job execution:
     * - running duration = time between previous job execution and instance termination
     * 2. If the instance was created after the previous job execution:
     * - running duration = time between instance creation and instance termination
     *
     * @param instanceV2Entity The terminated instance entity to calculate GPU usage for
     * @return double The GPU usage duration in hours with 2 decimal places
     */
    private InstanceInfoForGpuUsage getDurationOfTerminatedInstance(InstanceV2Entity instanceV2Entity) {

        Instant creationTime = TimeUtils.getInstantFromUuid(instanceV2Entity.getCreateTimeuuid());
        Instant terminationTime = instanceV2Entity.getInstanceUpdateTime();
        double instanceRunningDuration = 0;

        // TODO(NVCFSPOT-1477): Use previous job execution time from DB
        // Assuming the previous job was ran at nth hour
        // eg now() = 2025-05-12T20:50:45.167778Z, previousJobExecutionTime = 2025-05-12T20:00:00Z
        Instant previousJobExecutionTime = gpuUsageEventServiceHelper.getInstantNow().truncatedTo(ChronoUnit.HOURS);


        // The instance is started before previous job and terminated, gpuUsage = terminationTime - previousJobExecutionTime
        if (creationTime.isBefore(previousJobExecutionTime)) {
            instanceRunningDuration = getDurationInHours(previousJobExecutionTime,
                                                              terminationTime);
        } else {

            // The instance is started after previous job and still terminated, gpuUsage = terminationTime- creationTime
            instanceRunningDuration = getDurationInHours(creationTime, terminationTime);
        }

        return InstanceInfoForGpuUsage.builder()
                .instanceRunningDuration(instanceRunningDuration)
                .isInstanceCreatedBeforePreviousJob(creationTime.isBefore(previousJobExecutionTime))
                .terminationTime(terminationTime)
                .previousJobExecutionTime(previousJobExecutionTime)
                .creationTime(creationTime)
                .currentJobExecutionTime(null)
                .build();
    }

    private double getDurationInHours(Instant startTime, Instant endTime) {
        double durationInMins = Duration.between(startTime, endTime).toMinutes();
        return Math.round(durationInMins * 100.0 / 60.0) / 100.0;
    }

    private void sendGpuUsageEvent(
            InstanceV2Entity entity, double gpuUsageInHours,
            CloudProvider cloudProvider, int gpuCount,
            InstanceInfoForGpuUsage instanceInfoForGpuUsage) {

        InstanceLaunchSpecificationInfo instanceInfo = getInstanceLaunchSpecification(entity);

        Map<String, Object> metaData = new HashMap<>();
        addInstanceInfoForGpuUsageInTelemetry(metaData, instanceInfoForGpuUsage);
        addToMap(metaData, "gpuCount", gpuCount);

        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.GPU_USAGE_PER_INSTANCE.toString())
                .withGpuUsageInHours(gpuUsageInHours)
                .withInstanceId(entity.getInstanceId())
                .withInstanceState(entity.getInstanceStateName().getStateName())
                .withCloudProvider(cloudProvider)

                // Account details
                .withNcaId(instanceInfo.getNcaId())
                .withNcaIdPartnerName(instanceInfo.getNcaIdAccountName())

                // Function details
                .withFunctionId(instanceInfo.getFunctionId())
                .withFunctionName(instanceInfo.getFunctionName())

                // GPU details
                .withGpuName(instanceInfo.getGpu())
                .withInstanceType(instanceInfo.getInstanceType())

                // Task details
                .withTaskId(instanceInfo.getTaskId())
                .withTaskName(instanceInfo.getTaskName())

                // Reservation details
                .withReservationId(instanceInfo.getReservationId())
                .withCapacityType(instanceInfo.getCapacityType())

                // GPU usage metadata
                .withMetadata(metaData)
                .withZoneName(entity.getZone())
                .withRequestId(entity.getRequestId());

        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    @VisibleForTesting
    CloudProvider findCloudProvider(
            Map<String, ClusterProviderEnum> cloudProviderCache,
            InstanceV2Entity instanceV2Entity) {

        // Using cloudProvider from instanceV2Entity if available
        CloudProvider cloudProvider = getCloudProvider(instanceV2Entity);
        if (cloudProvider != null) {
            return cloudProvider;
        }

        // TODO: Once we have all instances updated with cloudProvider, then we can remove fall back logic
        // Fall back logic for finding cloudProvider
        String clusterId = instanceV2Entity.getZone();
        ClusterProviderEnum clusterProviderEnum = cloudProviderCache.computeIfAbsent(clusterId,
                                                                                     this::getClusterProviderEnum);
        cloudProvider = CloudProvider.getCloudProviderFromClusterProvider(
                clusterProviderEnum);

        if (cloudProvider == null) {
            log.info("Event: {} Failed to fetch cloud Provider for cluster {}",
                     GPU_USAGE_EVENT_NAME,
                     clusterId);
            return null;
        }

        return cloudProvider;
    }

    @VisibleForTesting
     int getGpuCountAssociatedToInstance(CloudProvider cloudProvider,
                                                InstanceV2Entity instanceV2Entity) {

        if (instanceV2Entity.getGpuCountPerInstance() != null
                && instanceV2Entity.getGpuCountPerInstance() != 0) {
            return instanceV2Entity.getGpuCountPerInstance();
        }

        // TODO: Once we have all instances updated with gpuCountPerInstance, then we can remove fall back logic
        // Fall back logic for finding cloudProvider
        String instanceType = instanceV2Entity.getInstanceType();
        if (cloudProvider != null) {
            if (computePlatformService.isComputePlatformProvider(cloudProvider)) {
                return getConfiguredGpuCount(instanceType);
            }
            return getGpuCountFromInstanceType(instanceType);
        }
        log.info("Event: {} for instanceType {} cloudProvider is null, considering gpuCount as 1",
                 GPU_USAGE_EVENT_NAME, instanceType);
        return 1;
    }

    /**
     * Returns the number of GPUs for an instance type based on the predefined configuration mapping.
     * If the instance type is not found in the mapping, returns 1 as default.
     *
     * @param instanceType The instance type to lookup
     * @return The number of GPUs for the instance type, defaults to configured default if not found
     */
    @VisibleForTesting
    int getConfiguredGpuCount(String instanceType) {
        return instanceTypeConfigurationProperties.getGpuCountForInstanceType(instanceType);
    }

    /**
     * Calculates the number of GPUs for an instance type by parsing its naming pattern.
     * <p>
     * The function handles various instance type formats:
     * 1. If '_' is not present in instanceType name, returns 1 as default
     * 2. Looks for patterns like '2x' or 'x2' in the last part after underscore
     * 3. Multiplies all found numbers to get total GPU count
     * <p>
     * Examples:
     * - "instance_2x" -> returns 2
     * - "instance_x2" -> returns 2
     * - "instance_2x.4x" -> returns 8
     * - "instance_2x.8x" -> returns 16
     * - "instance" -> returns 1 (no underscore)
     *
     * @param instanceType The instance type string to parse
     * @return The number of GPUs, defaults to 1 if parsing fails
     */
    public int getGpuCountFromInstanceType(String instanceType) {
        try {
            // If _ is not present in instanceType name then returning 1 as default
            if (!instanceType.contains("_")) {
                log.error(
                        "Event: {} Error processing instance type, _ not present in instanceType '{}'",
                        GPU_USAGE_EVENT_NAME, instanceType);
                return 1;
            }

            // Get the substring after the last underscore
            String lastPart = instanceType.substring(instanceType.lastIndexOf('_') + 1);
            
            // Find all numbers before 'x' (e.g., 2x) and after 'x' (e.g., x2)
            Pattern pattern = Pattern.compile("(\\d+)x|x(\\d+)");
            List<Integer> numbers = getIntegers(pattern, lastPart);

            if (numbers.isEmpty()) {
                return 1;
            }
            if (numbers.size() == 1) {
                return numbers.getFirst();
            }

            int result = 1;
            for (int n : numbers) {
                result *= n;
            }
            return result;
        } catch (Exception e) {
            log.error("Event: {} error processing instance type '{}': {}", GPU_USAGE_EVENT_NAME,
                      instanceType, e.getMessage());
            return 1;
        }
    }

    private static @NotNull List<Integer> getIntegers(Pattern pattern, String lastPart) {
        Matcher matcher = pattern.matcher(lastPart);

        List<Integer> numbers = new ArrayList<>();
        while (matcher.find()) {
            String beforeX = matcher.group(1);
            String afterX = matcher.group(2);

            if (beforeX != null) {
                numbers.add(Integer.parseInt(beforeX));
            }
            if (afterX != null) {
                numbers.add(Integer.parseInt(afterX));
            }
        }
        return numbers;
    }

    private @Nullable ClusterProviderEnum getClusterProviderEnum(String key) {
        Optional<ClusterEntity> optionalClusterEntity = clusterRepository.getClusterInfoByClusterId(
                key, true);
        return optionalClusterEntity.map(ClusterEntity::getClusterProvider)
                .orElse(null);
    }

    private void sendGpuUsageFindingFailedEvent(
            String message, String instanceId, SpotInstanceInternalState state) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withEventName(
                                                                   GPU_USAGE_PER_INSTANCE_EVENT_FAILED.toString())
                                                           .withError(String.format(
                                                                   "gpuUsage event sending failed, error: %s", message))
                                                           .withInstanceState(state.getStateName())
                                                           .withInstanceId(instanceId)));
    }

    private void sendInvalidGpuUsageEvent(
            InstanceV2Entity instanceV2Entity,
            InstanceInfoForGpuUsage instanceInfoForGpuUsage, int gpuCount) {

        Map<String, Object> metaData = new HashMap<>();
        addInstanceInfoForGpuUsageInTelemetry(metaData, instanceInfoForGpuUsage);
        addToMap(metaData, "gpuCount", gpuCount);

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withEventName(
                                                                   RUNNING_DURATION_FINDING_FOR_GPU_USAGE_FAILED.toString())
                                                           .withInstanceId(
                                                                   instanceV2Entity.getInstanceId())
                                                           .withMetadata(metaData)
                                                           .withInstanceState(instanceV2Entity.getInstanceStateName().getStateName())
                                                           .withError("Calculated gpuHours is negative")));

        log.error("Event: {} GPU usage hour calculation failed for {} instances,"
                          + " instanceId {}, metadata {}",
                  GPU_USAGE_EVENT_NAME,
                  instanceV2Entity.getInstanceStateName().getStateName(),
                  instanceV2Entity.getInstanceId(), metaData);
    }

    private void addToMap(Map<String, Object> metadata, String key, Object value) {
        if (value != null) {
            metadata.put(key, value);
        }
    }

    private void addInstanceInfoForGpuUsageInTelemetry(
            Map<String, Object> metadata,
            InstanceInfoForGpuUsage instanceInfoForGpuUsage) {

        addToMap(metadata, "instanceRunningDuration",
                 instanceInfoForGpuUsage.getInstanceRunningDuration());
        addToMap(metadata, "currentJobExecutionTime",
                 instanceInfoForGpuUsage.getCurrentJobExecutionTime());
        addToMap(metadata, "previousJobExecutionTime",
                 instanceInfoForGpuUsage.getPreviousJobExecutionTime());
        addToMap(metadata, "creationTime", instanceInfoForGpuUsage.getCreationTime());
        addToMap(metadata, "terminationTime", instanceInfoForGpuUsage.getTerminationTime());
        addToMap(metadata, "isInstanceCreatedBeforePreviousJob",
                 instanceInfoForGpuUsage.isInstanceCreatedBeforePreviousJob());
    }

    private InstanceLaunchSpecificationInfo getInstanceLaunchSpecification(
            InstanceV2Entity instanceV2Entity) {

        InstanceLaunchSpecificationInfoBuilder instanceInformation = InstanceLaunchSpecificationInfo.builder();

        LaunchSpecification launchSpecification = gpuUsageEventServiceHelper.parseRequestInfo(
                instanceV2Entity.getRequestRawData()).getLaunchSpecification();

        // org/Nvidia Cloud Account(NCA) information
        instanceInformation.ncaId(launchSpecification.getNcaId());
        instanceInformation.ncaIdAccountName(launchSpecification.getNcaIdAccountName());

        // Function information
        instanceInformation.functionId(launchSpecification.getFunctionId());
        instanceInformation.functionName(launchSpecification.getFunctionName());

        // GPU information
        instanceInformation.gpu(launchSpecification.getGpu());
        instanceInformation.instanceType(launchSpecification.getInstanceType());
        instanceInformation.cloudProvider(instanceV2Entity.getCloudProvider());

        // Reservation information
        String reservationId = getStringValueOfUuid(instanceV2Entity.getReservationId());
        instanceInformation.reservationId(reservationId);
        instanceInformation.capacityType(instanceV2Entity.getCapacityType());

        // Using fall back logic to set capacityType based on reservationId
        if (instanceInformation.capacityType == null) {
            instanceInformation.capacityType(CapacityType.SPOT.name());
            if (instanceInformation.reservationId != null) {
                instanceInformation.capacityType(CapacityType.RESERVED.name());
            }
        }

        // Task information
        // The request is for task if the maxRunTimeDuration is set
        if (StringUtils.isNotBlank(launchSpecification.getMaxRuntimeDuration())) {
            instanceInformation.taskId(getTaskId(launchSpecification, instanceV2Entity));
            instanceInformation.taskName(launchSpecification.getTaskName());
        }

        return instanceInformation.build();
    }

    private String getTaskId(
            LaunchSpecification launchSpecification, InstanceV2Entity instanceV2Entity) {

        // For newly created instances, taskId will be present in launchSpecification
        if (!StringUtils.isEmpty(launchSpecification.getTaskId())) {
            return launchSpecification.getTaskId();
        }

        // For older instances taskId won't be present in launchSpecification, fetching from request explicitly
        Optional<InstanceRequestV2Entity> optionalInstanceRequestEntity =
                instanceRequestV2Repository.findRequestById(instanceV2Entity.getRequestId());

        if (optionalInstanceRequestEntity.isPresent()
                && optionalInstanceRequestEntity.get().getTaskId() != null) {
            return optionalInstanceRequestEntity.get().getTaskId().toString();
        }

        log.warn("Event {}: Failed to fetch taskId for requestId {} from launchSpecification and InstanceRequestV2Entity",
                 GPU_USAGE_EVENT_NAME, instanceV2Entity.getRequestId());

        return null;
    }

}
