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
package com.nvidia.icms.outbound.sqs.model.byoc;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.nvidia.icms.outbound.sqs.model.FunctionDetails;
import com.nvidia.icms.outbound.sqs.model.TaskDetails;
import io.swagger.v3.oas.annotations.media.Schema;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

import jakarta.annotation.Nullable;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Unified SQS message model for BYOC function and task requests")
public class ByocSqsMessageModel {

    // Fields for function/task deployment
    @Schema(description = "RequestId representing all instances from this request",
            example = "11943833-929f-4ea9-ba07-4e1f1f321baa", requiredMode = Schema.RequiredMode.REQUIRED)
    private String requestId;

    @Schema(description = "Customer requesting instances", requiredMode = Schema.RequiredMode.REQUIRED)
    private String sub;

    @Schema(description = "Nvidia Cloud Account requesting the instances", requiredMode = Schema.RequiredMode.REQUIRED)
    private String ncaId;

    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Requested cluster group for instance creation", nullable = true)
    private String clusterGroup;

    @Schema(description = "Action for instance creation", allowableValues = {"RequestSpotInstances, RequestSpotInstancesForTask"}, requiredMode = Schema.RequiredMode.REQUIRED)
    private String action;

    @Schema(description = "Unique UUID presenting the SQS message", requiredMode = Schema.RequiredMode.REQUIRED)
    private String messageBatchId;

    // Fields related to tracing
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Trace parent used for trace linking", nullable = true)
    private String traceParent;

    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "TraceState used for trace linking", nullable = true)
    private Map<String, String> traceState;

    // Function details
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Details of the function", nullable = true)
    FunctionDetails functionDetails;

    // Task details
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Details of the task", nullable = true)
    private TaskDetails taskDetails;

    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Name of the account", nullable = true)
    private String accountName;

    // Function/task deployment details
    @Schema(description = "Details for launching instance", requiredMode = Schema.RequiredMode.REQUIRED)
    private ByocLaunchSpecification launchSpecification;

    // ========== DEPRECATED FIELDS ==========
    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Nvidia function ID requesting the instances", nullable = true, deprecated = true)
    private String functionId;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Version ID of Nvidia function requesting instances", nullable = true, deprecated = true)
    private String functionVersionId;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Number of instances requested in this request", nullable = true, deprecated = true)
    private Integer instanceCount;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Instance type of every instance in this request", nullable = true, deprecated = true)
    private String instanceType;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Name of the instance type of every instance in this request", nullable = true, deprecated = true)
    private String instanceTypeName;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Value of the instance type of every instance in this request", nullable = true, deprecated = true)
    private String instanceTypeValue;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "Type of GPU required for the instances", nullable = true, deprecated = true)
    private String gpuType;

    @Deprecated
    @Nullable
    @JsonInclude(JsonInclude.Include.NON_NULL)
    @Schema(description = "GPUs requested in for every instance in this request", nullable = true, deprecated = true)
    private Integer requestedGPUCount;

    @Builder
    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Schema(description = "Launch specification details for the instances")
    public static class ByocLaunchSpecification {

        // Fields for helm request
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "URL for the helm chart. Provided by NVCF", nullable = true)
        String helmChart;

        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Nullable
        @Schema(description = "Configuration per deployment-spec level to override/substitute the placeholders in values.yaml.", nullable = true)
        String configuration;

        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Nullable
        @Schema(description = "JSON string containing the function models payload forwarded by NVCF", nullable = true)
        String models;

        // Fields for model caching request
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "the name of the block device that would be used for caching models - should be equal to function version id.", nullable = true)
        String cacheHandle;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Rounded of storage class size of block device in Bytes", example = "73769040730L(~68.7Gi) will be rounded of to 82678120448L (77Gi)", nullable = true)
        Long cacheSize;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "indicates if NVCA should attempt to cache these artifacts or not", defaultValue = "false", nullable = true)
        Boolean cacheArtifacts;

        // Fields for function/task deployment
        @Schema(description = "Name of the cloud provider", requiredMode = Schema.RequiredMode.REQUIRED)
        String cloudProvider;

        @Schema(description = "Spot Environment making the request values: stage/prod", requiredMode = Schema.RequiredMode.REQUIRED)
        String spotEnvironment;

        @Schema(description = "ICMS environment making the request values: stage/prod", requiredMode = Schema.RequiredMode.REQUIRED)
        String icmsEnvironment;

        @Schema(description = "Base64 encoded string having function deploying details such as container images, secrets etc", requiredMode = Schema.RequiredMode.REQUIRED)
        String environment;

        @Schema(description = "Name of the instance type of every instance in this request", requiredMode = Schema.RequiredMode.REQUIRED)
        private String instanceTypeName;

        @Schema(description = "Value of the instance type of every instance in this request", requiredMode = Schema.RequiredMode.REQUIRED)
        private String instanceTypeValue;

        @Schema(description = "Number of instances requested in this request", requiredMode = Schema.RequiredMode.REQUIRED)
        private Integer instanceCount;

        @Schema(description = "Type of GPU required for the instances", requiredMode = Schema.RequiredMode.REQUIRED)
        private String gpuType;

        @Schema(description = "GPUs requested in for every instance in this request", requiredMode = Schema.RequiredMode.REQUIRED)
        private Integer requestedGPUCount;

        @Schema(description = "Container image to be used for the instances", requiredMode = Schema.RequiredMode.REQUIRED)
        private String containerImage;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "NVCF Deployment Id", nullable = true)
        private UUID deploymentId;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "NVCF GPU Specification Id", nullable = true)
        private UUID gpuSpecificationId;

        // Bring Your Own Observability (BYOO) fields
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Base64 encoded telemetries to be added to worker", nullable = true)
        private String telemetries;

        // Task specific
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Maximum runtime duration for the instances", nullable = true)
        private String maxRuntimeDuration;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Maximum queued duration for the instances", nullable = true)
        private String maxQueuedDuration;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Grace period duration before termination", nullable = true)
        private String terminationGracePeriodDuration;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Strategy for handling task results", nullable = true)
        private String resultHandlingStrategy;

        // Fields for detailed targeting
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Clusters used for detailed targeting of function", nullable = true)
        Set<String> clusters;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Regions used for detailed targeting of function", nullable = true)
        Set<String> regions;

        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Attributes used for detailed targeting of function", nullable = true)
        Set<String> attributes;

        // ========== DEPRECATED FIELDS ==========
        @Deprecated
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Name of the GPU", nullable = true, deprecated = true)
        String gpuName;

        @Deprecated
        @Nullable
        @JsonInclude(JsonInclude.Include.NON_NULL)
        @Schema(description = "Type of instance to be launched", nullable = true, deprecated = true)
        private String instanceType;
    }
}
