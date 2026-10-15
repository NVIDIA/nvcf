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
package com.nvidia.nvct.service.icms;

import com.fasterxml.jackson.annotation.JsonCreator;
import com.fasterxml.jackson.annotation.JsonProperty;
import tools.jackson.databind.PropertyNamingStrategies;
import tools.jackson.databind.annotation.JsonNaming;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.HealthInfo;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.InstanceState;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.Placement;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;
import lombok.RequiredArgsConstructor;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import org.apache.commons.lang3.StringUtils;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.service.annotation.DeleteExchange;
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.annotation.PostExchange;

public interface IcmsStubService {

    @Value
    @Jacksonized
    @Builder
    class CreateInstancesResponse {
        UUID requestId;
    }

    @PostExchange(value = "/v1/si?Action=RequestInstances",
            accept = "application/json",
            contentType = "application/x-www-form-urlencoded")
    CreateInstancesResponse createInstance(
            @RequestParam(value = "LaunchSpecification.Backend", required = false)
            String backend,
            @RequestParam(value = "LaunchSpecification.Clusters", required = false)
            Set<String> clusters,
            @RequestParam("LaunchSpecification.Gpu")
            String gpu,
            @RequestParam("LaunchSpecification.InstanceType")
            String instanceType,
            @RequestParam("LaunchSpecification.NcaId")
            String ncaId,
            @RequestParam("TaskDetails.AccountName")
            String accountName,
            @RequestParam("InstanceCount")
            int instanceCount,
            @RequestParam("LaunchSpecification.ContainerImage")
            String containerImage,
            @RequestParam("LaunchSpecification.HelmChart")
            String helmChart,
            @RequestParam(value = "LaunchSpecification.Configuration", required = false)
            String configuration,
            @RequestParam("LaunchSpecification.Environment")
            String environment,
            @RequestParam(value = "LaunchSpecification.ArtifactUrl", required = false)
            URI artifactUrl,
            @RequestParam("LaunchSpecification.CacheArtifacts")
            boolean cacheArtifacts,
            @RequestParam("LaunchSpecification.CacheHandle")
            String cacheHandle,
            @RequestParam(value = "LaunchSpecification.CacheSize", required = false)
            Long cacheSize,
            @RequestParam("TaskDetails.OwnerNcaId")
            String ownerNcaId,
            @RequestParam("TaskDetails.TaskId")
            UUID taskId,
            @RequestParam("TaskDetails.TaskName")
            String taskName,
            @RequestParam("LaunchSpecification.MaxRuntimeDuration")
            String maxRuntimeDuration,
            @RequestParam("LaunchSpecification.MaxQueuedDuration")
            String maxQueuedDuration,
            @RequestParam("LaunchSpecification.TerminationGracePeriodDuration")
            String terminationGracePeriodDuration,
            @RequestParam("LaunchSpecification.ResultHandlingStrategy")
            String resultHandlingStrategy,
            @RequestParam("LaunchSpecification.Telemetries")
            String telemetries,
            @RequestParam("LaunchSpecification.DeploymentId")
            UUID deploymentId,
            @RequestParam("LaunchSpecification.GpuSpecificationId")
            UUID gpuSpecificationId,
            @RequestParam(value = "LaunchSpecification.HelmValidationPolicy", required = false)
            String helmValidationPolicy);

    @Value
    @Jacksonized
    @Builder
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class GetInstancesResponse {

        List<InstanceRequest> instanceRequests;

        @Value
        @Jacksonized
        @Builder
        @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
        public static class InstanceRequest {

            Instant createTime;
            String instanceId;
            LaunchSpecification launchSpecification;
            String launchedAvailabilityZone;
            UUID instanceRequestId;
            State state;
            Status status;
            String instanceInterruptionBehavior;
            String cloudProvider;
            @Nullable
            InstanceState instanceState;
            @Nullable
            HealthInfo healthInfo;

            @Value
            @Jacksonized
            @Builder
            @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
            public static class LaunchSpecification {

                String instanceType;
                String gpu;
                String containerImage;
                Placement placement;
            }

            @Value
            @Jacksonized
            @Builder
            @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
            public static class Placement {

                String availabilityZone;
            }

            @Value
            @Jacksonized
            @Builder
            @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
            public static class Status {

                String code;
                String message;
                Instant updateTime;
            }

            @RequiredArgsConstructor
            public enum State {
                OPEN, ACTIVE, CLOSED, CANCELED, FAILED;

                public boolean isActive() {
                    return this == ACTIVE;
                }

                @JsonCreator
                public static State fromString(String state) {
                    return StringUtils.isBlank(state) ? null : State.valueOf(state.toUpperCase());
                }

            }

            @Value
            @Jacksonized
            @Builder
            @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
            public static class InstanceState {

                int code;
                String name;

                public boolean isStartingOrRunning() {
                    return "starting".equals(name) || "running".equals(name);
                }

                public boolean isRunning() {
                    return "running".equals(name);
                }
            }

            @Value
            @Jacksonized
            @Builder
            @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
            public static class HealthInfo {
                String errorLog;
            }
        }
    }

    @DeleteExchange("/v1/si?Action=TerminateInstances")
    GetInstancesResponse deleteInstances(
            @RequestParam("InstanceId") List<String> instanceIds);

    @DeleteExchange("/v1/si/accounts/{ncaId}/workloads/{workloadId}")
    void terminateInstancesByTaskId(
            @PathVariable("ncaId") String ncaId,
            @PathVariable("workloadId") UUID taskId);

    @Value
    @Jacksonized
    @Builder
    class ClusterGroupsResponse {
        List<ClusterGroup> clusterGroups;

        @Value
        @Jacksonized
        @Builder
        public static class ClusterGroup {
            UUID id;
            String name;
            String ncaId;
            List<String> authorizedNcaIds;
            List<Gpu> gpus;
            List<Cluster> clusters;

            @Value
            @Jacksonized
            @Builder
            public static class Gpu {
                String name;
                List<InstanceType> instanceTypes;

                @Value
                @Jacksonized
                @Builder
                public static class InstanceType {
                    String name;
                    String description;
                    @JsonProperty("default")
                    boolean defaultInstanceType;
                }
            }

            @Value
            @Jacksonized
            @Builder
            public static class Cluster {
                String k8sVersion;
                String id;
                String name;
            }
        }
    }

    @GetExchange("/v1/si/accounts/{ncaId}/clusterGroups")
    ClusterGroupsResponse getClusterGroups(
            @PathVariable("ncaId") String ncaId,
            @RequestParam("instanceTypeUsage") InstanceUsageTypeEnum instanceUsage);

    @Value
    @Jacksonized
    @Builder
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class DescribeInstancesResponse {

        List<Instance> instances;

        @Value
        @Jacksonized
        @Builder
        @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
        public static class Instance {

            String instanceId;
            Set<String> instanceIps;
        }
    }

    @GetExchange("/v1/si?Action=DescribeInstances")
    DescribeInstancesResponse describeInstances(@RequestParam("InstanceId") Set<String> instanceIds);

    @Value
    @Jacksonized
    @Builder
    class InstanceTypeDetails {
        String name;
        String value;
        String description;
        int cpuCores;
        String systemMemory;
        String gpuMemory;
        int gpuCount = 1;
        Set<String> clusters;
        Set<String> regions;
        Set<String> attributes;
        String gpuName;
        Boolean defaultable;
        String cpuArch;
        String os;
        String driverVersion;
        String storage;
    }

    @Value
    @Jacksonized
    @Builder
    class ClusterResponse {
        String clusterName;
        String clusterGroupName;
        String ncaId;
        Set<String> authorizedNCAIds;
        String cloudProvider;
        String region;
        Set<GpuV4> gpus;
        String clusterId;
        String clusterGroupId;
        String status;

        @Value
        @Jacksonized
        @Builder
        public static class GpuV4 {
            String name;
            int capacity;
            Set<InstanceTypeDetails> instanceTypes;
        }
    }

    @GetExchange(
            "/v1/si/accounts/{ncaId}/clusters?includeAuthorizedClusters=true&includeGfnInAuthorizedClusters=true")
    List<ClusterResponse> getClusters(
            @PathVariable("ncaId") String ncaId,
            @RequestParam("instanceTypeUsage") InstanceUsageTypeEnum instanceUsage);

    @Value
    @Jacksonized
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    @Builder
    class Instances {
        List<Instance> Instances;
    }

    @Value
    @Jacksonized
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    @Builder
    class Instance {
        Instant createTime;
        String imageId;
        String containerImage;
        String instanceId;
        String cloudProvider;
        String instanceType;
        Placement placement;
        InstanceState state;
        HealthInfo healthInfo;
        String launchRequestId;
        Set<String> instanceIps;
        String capacityType;
        UUID deploymentId;
        UUID gpuSpecificationId;
    }

    @GetExchange("/v1/si/accounts/{ncaId}/deployments/{deploymentId}/instances")
    Instances terminateInstancesByTaskId(
            @PathVariable("ncaId") String ncaId,
            @PathVariable("deploymentId") UUID taskId,
            @RequestParam("IncludeTerminated") boolean includeTerminated,
            @RequestParam("UseConciseName") boolean useConciseName,
            @RequestParam("ExpiredAckedInstances") boolean expiredAckedInstances);
}
