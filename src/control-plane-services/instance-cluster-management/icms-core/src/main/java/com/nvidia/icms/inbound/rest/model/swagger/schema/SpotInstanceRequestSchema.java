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
package com.nvidia.icms.inbound.rest.model.swagger.schema;

import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import io.swagger.v3.oas.annotations.media.Schema;
import java.time.Duration;
import java.util.Collection;
import java.util.Set;
import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.apache.commons.lang3.StringUtils;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Schema(description = "Requesting new Instances")
public class SpotInstanceRequestSchema {

    @Schema(name = "Action", description = "Action for instance create request",
            allowableValues = {"RequestSpotInstances", "RequestSpotInstancesForTask",
                    "RequestInstances", "RequestInstancesForTask"},
            requiredMode = Schema.RequiredMode.REQUIRED)
    SpotInstanceRequestAction action;

    @Schema(name = "InstanceCount", description = "Number of instances to be created", requiredMode = Schema.RequiredMode.REQUIRED)
    int instanceCount;

    @Schema(name = "LaunchSpecification.InstanceType",
            description = "Instance type for the instances to be created",
            requiredMode = Schema.RequiredMode.REQUIRED)
    String instanceType;

    @Schema(name = "LaunchSpecification.ContainerImage", description = "Container image for the instances to be created")
    String containerImage;

    @Schema(name = "LaunchSpecification.Environment", description =
            "Base64 encoded string where key and value are separated by '='" +
                    " and such pairs are separated by new line. This can be used to send credentials.")
    String environment;

    @Schema(name = "LaunchSpecification.Placement.AvailabilityZone", description =
            "Availability zone where " +
                    "instances needs to be created")
    String availabilityZone;

    @Schema(name = "LaunchSpecification.Backend", description = "Backend where " +
            "instances needs to be created")
    String backend;

    @Schema(name = "LaunchSpecification.Gpu", description = "Gpu which needs to be attached " +
            "to instance")
    String gpu;

    @Schema(name = "LaunchSpecification.NcaId", description = "Id of the cloud account for which " +
            "instances needs to be created")
    String ncaId;

    @Schema(name = "LaunchSpecification.HelmChart", description =
            "URL for the helm chart - if specified, " +
                    "this helm chart will be deployed for a mini service")
    String helmChart;

    @Schema(name = "LaunchSpecification.Configuration", description =
            "Configuration per deployment-spec " +
                    "level to override/substitute the placeholders in values.yaml")
    String configuration;

    @Schema(name = "LaunchSpecification.Models", description =
            "JSON string containing the function models payload forwarded by NVCF")
    String models;

    @Schema(name = "LaunchSpecification.CacheArtifacts", description =
            "indicates if NVCA should attempt to cache these artifacts or not")
    boolean cacheArtifacts;

    @Schema(name = "LaunchSpecification.CacheHandle", description =
            "the name of the block device that" +
                    " would be used for caching models - should be equal to function version id")
    String cacheHandle;

    @Schema(name = "LaunchSpecification.CacheSize", description = "Storage class size of block device")
    Long cacheSize;

    @Schema(name = "FunctionDetails.FunctionId", description = "Function id to which the requested instance is associated")
    UUID functionId;

    @Schema(name = "FunctionDetails.FunctionName", description = "Function name")
    @Nullable
    String functionName;

    @Schema(name = "FunctionDetails.FunctionVersionId", description = "Function version id to which the requested instance is associated")
    UUID functionVersionId;

    @Schema(name = "FunctionDetails.OwnerNcaId", description = "Owner nca id of function to which the requested instance is associated")
    String ownerNcaId;

    @Schema(name = "FunctionDetails.FunctionType", description = "Indicates whether the function is a default, streaming, or LLM function")
    FunctionType functionType;

    @Schema(name = "LaunchSpecification.Clusters", description = "Targeted clusters name. " +
            "All deployed instances will be within the clusters list")
    Set<String> clusters;

    @Schema(name = "LaunchSpecification.Regions", description = "List of targeted regions. " +
            "All deployed instances will be within clusters from the region list")
    Set<String> regions;

    @Schema(name = "LaunchSpecification.Attributes", description = "Cluster attributes. " +
            "All deployed instances will be with in clusters with specified attributes.")
    Set<String> attributes;

    @Schema(name = "LaunchSpecification.MaxRuntimeDuration",
            description = "Optional String in ISO 8601 duration format. If specified, Utils Container should terminate the task after the specified time period has elapsed. Otherwise, let the task run forever.")
    Duration maxRuntimeDuration;

    @Schema(name = "LaunchSpecification.MaxQueuedDuration", description = "ISO 8601 duration format. Maximum amount of time that the task can stay queued waiting for a worker.")
    Duration maxQueuedDuration;

    @Schema(name = "LaunchSpecification.TerminationGracePeriodDuration",
            description = "ISO 8601 duration format. Maximum amount of time that the task instance should be given to gracefully terminate. Should not be more than LaunchSpecification.MaxRuntimeDuration")
    Duration terminationGracePeriodDuration;

    @Schema(name = "LaunchSpecification.ResultHandlingStrategy", description = "Result handling strategy for NVCT")
    ResultHandlingStrategy resultHandlingStrategy;

    @Schema(name = "TaskDetails.TaskId", description = "NVCT Task Id")
    UUID taskId;

    @Schema(name = "TaskDetails.AccountName", description = "Account name corresponding to task")
    String accountName;

    @Schema(name = "TaskDetails.TaskName", description = "Task name")
    @Nullable
    String taskName;

    @Schema(name = "TaskDetails.OwnerNcaId", description = "Owner nca id of task to which the requested instance is associated")
    String ownerNcaIdForTask;

    @Schema(name = "LaunchSpecification.Telemetries", description = "Base64 encoded telemetries to be added to worker")
    String telemetries;

    @Schema(name = "LaunchSpecification.DeploymentId", description = "Function or Task Deployment id")
    UUID deploymentId;

    @Schema(name = "LaunchSpecification.GpuSpecificationId", description = "Function or Task Gpu Specification id")
    UUID gpuSpecificationId;

    public String getLoggingId() {
        if (taskId != null) {
            return String.format("TaskId %s | DeploymentId %s | NcaId %s", this.getTaskId(),  this.getDeploymentId(), this.getNcaId());
        }
        else {
            return String.format("FunctionId %s | DeploymentId %s | NcaId %s", this.getFunctionId(),  this.getDeploymentId(), this.getNcaId());
        }
    }

    /* Following fields should not be involved in toString due to security reasons
    1. Telemetries
    2. Environment
     */
    @Override
    public String toString() {
        StringBuilder response = new StringBuilder();

        // Basic request info
        appendIfNotNull(response, "Action", getAction().getRequestAction());
        appendIfPositive(response, "InstanceCount", getInstanceCount());

        // Instance configuration
        appendIfNotBlank(response, "InstanceType", getInstanceType());
        appendIfNotBlank(response, "Gpu", getGpu());
        appendIfNotBlank(response, "ContainerImage", getContainerImage());

        // Account and backend info
        appendIfNotBlank(response, "NcaId", getNcaId());
        appendIfNotBlank(response, "Backend", getBackend());
        appendIfNotBlank(response, "AvailabilityZone", getAvailabilityZone());

        // Function details
        appendIfNotNull(response, "FunctionId", getFunctionId());
        appendIfNotBlank(response, "FunctionName", getFunctionName());
        appendIfNotNull(response, "FunctionVersionId", getFunctionVersionId());
        appendIfNotBlank(response, "OwnerNcaId", getOwnerNcaId());
        appendIfNotNull(response, "FunctionType", getFunctionType());

        // Task details
        appendIfNotNull(response, "TaskId", getTaskId());
        appendIfNotBlank(response, "TaskName", getTaskName());
        appendIfNotBlank(response, "AccountName", getAccountName());
        appendIfNotBlank(response, "OwnerNcaIdForTask", getOwnerNcaIdForTask());

        // Deployment info
        appendIfNotNull(response, "DeploymentId", getDeploymentId());
        appendIfNotNull(response, "GpuSpecificationId", getGpuSpecificationId());

        // Targeting info
        appendIfNotEmpty(response, "Clusters", getClusters());
        appendIfNotEmpty(response, "Regions", getRegions());
        appendIfNotEmpty(response, "Attributes", getAttributes());

        // Helm chart and configuration
        appendIfNotBlank(response, "HelmChart", getHelmChart());
        appendIfNotBlank(response, "Configuration", getConfiguration(), "[Present]");
        appendIfNotBlank(response, "Models", getModels(), "[Present]");

        // Cache configuration
        appendIfTrue(response, "CacheArtifacts", isCacheArtifacts());
        appendIfNotBlank(response, "CacheHandle", getCacheHandle());
        appendIfNotNull(response, "CacheSize", getCacheSize());

        // Duration settings
        appendIfNotNull(response, "MaxRuntimeDuration", getMaxRuntimeDuration());
        appendIfNotNull(response, "MaxQueuedDuration", getMaxQueuedDuration());
        appendIfNotNull(response, "TerminationGracePeriodDuration", getTerminationGracePeriodDuration());
        appendIfNotNull(response, "ResultHandlingStrategy", getResultHandlingStrategy());

        // Remove trailing " | " if present
        if (response.length() > 0 && response.toString().endsWith(" | ")) {
            response.setLength(response.length() - 3);
        }

        return response.toString();
    }

    /**
     * Appends a field to the StringBuilder if the value is not null
     */
    private void appendIfNotNull(StringBuilder sb, String fieldName, Object value) {
        if (value != null) {
            sb.append(fieldName).append(": ").append(value).append(" | ");
        }
    }

    /**
     * Appends a field to the StringBuilder if the value is not blank
     */
    private void appendIfNotBlank(StringBuilder sb, String fieldName, String value) {
        if (StringUtils.isNotBlank(value)) {
            sb.append(fieldName).append(": ").append(value).append(" | ");
        }
    }

    /**
     * Appends a field to the StringBuilder if the value is not blank, using a custom display value
     */
    private void appendIfNotBlank(StringBuilder sb, String fieldName, String value, String displayValue) {
        if (StringUtils.isNotBlank(value)) {
            sb.append(fieldName).append(": ").append(displayValue).append(" | ");
        }
    }

    /**
     * Appends a field to the StringBuilder if the value is greater than 0
     */
    private void appendIfPositive(StringBuilder sb, String fieldName, int value) {
        if (value > 0) {
            sb.append(fieldName).append(": ").append(value).append(" | ");
        }
    }

    /**
     * Appends a field to the StringBuilder if the value is true
     */
    private void appendIfTrue(StringBuilder sb, String fieldName, boolean value) {
        if (value) {
            sb.append(fieldName).append(": true | ");
        }
    }

    /**
     * Appends a field to the StringBuilder if the collection is not null and not empty
     */
    private void appendIfNotEmpty(StringBuilder sb, String fieldName, Collection<?> value) {
        if (value != null && !value.isEmpty()) {
            sb.append(fieldName).append(": ").append(value).append(" | ");
        }
    }
}
