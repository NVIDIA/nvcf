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
package com.nvidia.icms.service.telemetry.model;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.outbound.ngc.model.AccountType;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import jakarta.annotation.Nullable;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import jakarta.validation.constraints.NotNull;
import lombok.Data;
import org.apache.commons.lang3.StringUtils;

@Data
// TODO: Add fields needed for events
public class GenericMetric {

    private String instanceCreationRequestBody;

    private String eventName;
    private String customer;
    private Double eventTimestamp;

    private String requestId;

    private String instanceId;

    private String reasonForTermination;

    private String error;

    private Integer instanceCount;

    private String instanceState;

    private String requestState;

    // Use camel casing for keys
    private Map<String, Object> metadata;

    private String zoneName;

    private String env;

    private String region;

    //  Time difference between(instance was in running state - instance was terminated)
    private Long instanceLifeTime;

    //  Time difference between user request and provider pickup.
    private Long instanceRequestAcceptanceTime;

    // Time difference between (zone picks up the request - instance started running)
    private Long instanceWaitingTime;

    private Integer responseStatus;

    private String cloudProvider;

    private String cloudHealthStatus;

    private String messageBatchId;

    private Integer sqsMessageAcknowledgeInstanceCount;

    private String podName;

    private String resourceProvider;

    private String functionId;

    private String functionVersionId;

    private String ncaId;

    private UUID deploymentId;

    private UUID gpuSpecificationId;

    private String instanceType;

    private String taskId;

    private String createdTimeUuid;

    private String timeStampFromUuid;

    private String action;

    private Integer httpCode;

    private String regionName;

    // This param will store clusterName for NVCA clusters and zoneName for non-BYOC clusters
    private String clusterName;

    private String gpuName;

    private Double gpuUsageInHours;

    private String functionName;

    private String ncaIdPartnerName;

    private String taskName;

    private String reservationId;

    private String clusterId;

    private String ncaIdAccountType;

    private String reservationStatus;

    private Instant reservationStartTime;

    private Instant reservationEndTime;

    private Integer reservedGpuCount;

    private Double availableGpuCount;

    private String capacityType;

    private Instant instanceExpirationTime;

    private Instant instanceCreateTime;

    public GenericMetric withCloudHealthStatus(@Nullable CloudHealthStatus cloudHealthStatus) {
        if (cloudHealthStatus != null) {
            this.cloudHealthStatus = cloudHealthStatus.toString();
        }
        return this;
    }

    public GenericMetric withCapacityType(@Nullable CapacityType capacityType) {
        if (capacityType != null) {
            this.capacityType = capacityType.toString();
        }
        return this;
    }

    public GenericMetric withCapacityType(@Nullable String capacityType) {
        if (capacityType != null) {
            this.capacityType = capacityType;
        }
        return this;
    }

    public GenericMetric withGpuName(@Nullable String gpuName) {
        if (!StringUtils.isBlank(gpuName)) {
            this.gpuName = gpuName;
        }
        return this;
    }

    public GenericMetric withGpuUsageInHours(@Nullable Double gpuUsageInHours) {
        if (gpuUsageInHours != null) {
            this.gpuUsageInHours = gpuUsageInHours;
        }
        return this;
    }

    public GenericMetric withFunctionName(@Nullable String functionName) {
        if (!StringUtils.isBlank(functionName)) {
            this.functionName = functionName;
        }
        return this;
    }

    public GenericMetric withNcaIdPartnerName(@Nullable String ncaIdPartnerName) {
        if (!StringUtils.isBlank(ncaIdPartnerName)) {
            this.ncaIdPartnerName = ncaIdPartnerName;
        }
        return this;
    }

    public GenericMetric withNcaIdAccountType(@Nullable AccountType ncaIdAccountType) {
        if (ncaIdAccountType != null) {
            this.ncaIdAccountType = ncaIdAccountType.name();
        }
        return this;
    }

    public GenericMetric withTaskName(@Nullable String taskName) {
        if (!StringUtils.isBlank(taskName)) {
            this.taskName = taskName;
        }
        return this;
    }

    public GenericMetric withEventName(String eventName) {
        this.eventName = eventName;
        return this;
    }

    public GenericMetric withCustomer(String customer) {
        this.customer = customer;
        return this;
    }

    public GenericMetric withRequestId(String requestId) {
        this.requestId = requestId;
        return this;
    }

    public GenericMetric withInstanceId(String instanceId) {
        this.instanceId = instanceId;
        return this;
    }

    public GenericMetric withReasonForTermination(String reasonForTermination) {
        this.reasonForTermination = reasonForTermination;
        return this;
    }

    public GenericMetric withError(String error) {
        if (!StringUtils.isEmpty(error)) {
            this.error = error;
        }
        return this;
    }

    public GenericMetric withEventTimestamp(double eventTimestamp) {
        this.eventTimestamp = eventTimestamp;
        return this;
    }

    public GenericMetric withMetadata(Map<String, Object> metadata) {
        if (metadata != null) {
            this.metadata = metadata;
        }
        return this;
    }


    public GenericMetric withInstanceState(String instanceState) {
        this.instanceState = instanceState;
        return this;
    }

    public GenericMetric withRequestState(String requestState) {
        this.requestState = requestState;
        return this;
    }

    public GenericMetric withZoneName(String zoneName) {
        if (!StringUtils.isEmpty(zoneName)) {
            this.zoneName = zoneName;
        }
        return this;
    }

    public GenericMetric withEnv(String env) {
        this.env = env;
        return this;
    }

    public GenericMetric withInstanceLifeTime(long instanceLifeTime) {
        this.instanceLifeTime = instanceLifeTime;
        return this;
    }

    public GenericMetric withInstanceRequestAcceptanceTime(long instanceRequestAcceptanceTime) {
        this.instanceRequestAcceptanceTime = instanceRequestAcceptanceTime;
        return this;
    }

    public GenericMetric withInstanceWaitingTime(long instanceWaitingTime) {
        this.instanceWaitingTime = instanceWaitingTime;
        return this;
    }

    public GenericMetric withInstanceCount(int instanceCount) {
        this.instanceCount = instanceCount;
        return this;
    }

    public GenericMetric withCloudProvider(@Nullable CloudProvider cloudProvider) {
        if (cloudProvider != null) {
            this.cloudProvider = cloudProvider.toString();
        }
        return this;
    }

    public GenericMetric withMessageBatchId(@Nullable String messageBatchId) {
        if (!StringUtils.isEmpty(messageBatchId)) {
            this.messageBatchId = messageBatchId;
        }
        return this;
    }

    public GenericMetric withSqsMessageAcknowledgeInstanceCount(
            @Nullable Integer instanceCount) {
        if (instanceCount != null) {
            this.sqsMessageAcknowledgeInstanceCount = instanceCount;
        }
        return this;
    }

    public GenericMetric withPodName(@Nullable String podName) {
        if (StringUtils.isNotBlank(podName)) {
            this.podName = podName;
        }
        return this;
    }

    public GenericMetric withResourceProvider(ResourceProvider resourceProvider) {
        if (resourceProvider != null) {
            this.resourceProvider = resourceProvider.toString();
        }
        return this;
    }

    public GenericMetric withFunctionId(@Nullable UUID functionId) {
        if (functionId != null && StringUtils.isNotBlank(functionId.toString())) {
            this.functionId = functionId.toString();
        }
        return this;
    }

    public GenericMetric withFunctionId(@Nullable String functionId) {
        if (StringUtils.isNotBlank(functionId)) {
            this.functionId = functionId;
        }
        return this;
    }

    public GenericMetric withFunctionVersionId(@Nullable UUID functionVersionId) {
        if (functionVersionId != null && StringUtils.isNotBlank(functionVersionId.toString())) {
            this.functionVersionId = functionVersionId.toString();
        }
        return this;
    }

    public GenericMetric withFunctionVersionId(@Nullable String functionVersionId) {
        if (StringUtils.isNotBlank(functionVersionId)) {
            this.functionVersionId = functionVersionId;
        }
        return this;
    }

    public GenericMetric withNcaId(@Nullable String ncaId) {
        if (StringUtils.isNotBlank(ncaId)) {
            this.ncaId = ncaId;
        }
        return this;
    }

    public GenericMetric withDeploymentId(@Nullable UUID deploymentId) {
        if (deploymentId != null) {
            this.deploymentId = deploymentId;
        }
        return this;
    }

    public GenericMetric withGpuSpecificationId(@Nullable UUID gpuSpecificationId) {
        if (gpuSpecificationId != null) {
            this.gpuSpecificationId = gpuSpecificationId;
        }
        return this;
    }

    public GenericMetric withInstanceType(@Nullable String instanceType) {
        if (StringUtils.isNotBlank(instanceType)) {
            this.instanceType = instanceType;
        }
        return this;
    }

    public GenericMetric withTaskId(@Nullable String taskId) {
        if (StringUtils.isNotBlank(taskId)) {
            this.taskId = taskId;
        }
        return this;
    }

    public GenericMetric withCreatedTimeUuid(@Nullable String createdTimeUuid) {
        if (StringUtils.isNotBlank(createdTimeUuid)) {
            this.createdTimeUuid = createdTimeUuid;
        }
        return this;
    }

    public GenericMetric withTimeStampFromUuid(@Nullable String timeStampFromUuid) {
        if (StringUtils.isNotBlank(timeStampFromUuid)) {
            this.timeStampFromUuid = timeStampFromUuid;
        }
        return this;
    }

    public GenericMetric withAction(@Nullable String action) {
        if (StringUtils.isNotBlank(action)) {
            this.action = action;
        }
        return this;
    }


    public GenericMetric withClusterName(@Nullable String clusterName) {
        if (StringUtils.isNotBlank(clusterName)) {
            this.clusterName = clusterName;
        }
        return this;
    }

    public GenericMetric withHttpCode(@Nullable Integer httpCode) {
        if (httpCode != null) {
            this.httpCode = httpCode;
        }
        return this;
    }

    public GenericMetric withRegionName(@Nullable String regionName) {
        if (regionName != null) {
            this.regionName = regionName;
        }
        return this;
    }

    public GenericMetric withReservationId(@Nullable String reservationId) {
        if (reservationId != null) {
            this.reservationId = reservationId;
        }
        return this;
    }

    public GenericMetric withReservationId(@Nullable UUID reservationId) {
        if (reservationId != null) {
            this.reservationId = reservationId.toString();
        }
        return this;
    }

    public GenericMetric withClusterId(@Nullable String clusterId) {
        if (clusterId != null) {
            this.clusterId = clusterId;
        }
        return this;
    }

    public GenericMetric withReservationStatus(String reservationStatus) {
        this.reservationStatus = reservationStatus;
        return this;
    }

    public GenericMetric withReservationStartTime(Instant reservationStartTime) {
        this.reservationStartTime = reservationStartTime;
        return this;
    }

    public GenericMetric withReservationEndTime(Instant reservationEndTime) {
        this.reservationEndTime = reservationEndTime;
        return this;
    }

    public GenericMetric withReservedGpuCount(@Nullable Integer reservedGpuCount) {
        if (reservedGpuCount != null) {
            this.reservedGpuCount = reservedGpuCount;
        }
        return this;
    }

    public GenericMetric withAvailableGpuCount(@Nullable Double availableGpuCount) {
        if (availableGpuCount != null) {
            this.availableGpuCount = availableGpuCount;
        }
        return this;
    }

    public GenericMetric withInstanceCreationRequestBody(@NotNull String instanceCreationRequestBody) {
        this.instanceCreationRequestBody = instanceCreationRequestBody;
        return this;
    }

    public GenericMetric withInstanceExpirationTime(@Nullable Instant instanceExpirationTime) {
        if (instanceExpirationTime != null) {
            this.instanceExpirationTime = instanceExpirationTime;
        }
        return this;
    }

    public GenericMetric withInstanceCreateTime(@Nullable Instant instanceCreateTime) {
        if (instanceCreateTime != null) {
            this.instanceCreateTime = instanceCreateTime;
        }
        return this;
    }
}
