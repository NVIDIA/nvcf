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
package com.nvidia.nvcf.icms.client;

import static com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest.HealthInfo;
import static com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest.InstanceState;
import static com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest.Placement;
import static org.springframework.http.MediaType.APPLICATION_FORM_URLENCODED_VALUE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.fasterxml.jackson.annotation.JsonCreator;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Map;
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
import tools.jackson.databind.PropertyNamingStrategies;
import tools.jackson.databind.annotation.JsonNaming;

public interface IcmsStubService {

    @Value
    @Jacksonized
    @Builder
    class CreateInstancesResponse {

        UUID requestId;
    }

    @PostExchange(url = "/v1/si?Action=RequestInstances",
                  accept = APPLICATION_JSON_VALUE,
                  contentType = APPLICATION_FORM_URLENCODED_VALUE)
    CreateInstancesResponse createInstance(
            @RequestParam(value = "LaunchSpecification.Backend",
                          required = false) String backend,
            @RequestParam("LaunchSpecification.Gpu") String gpu,
            @RequestParam("LaunchSpecification.InstanceType") String instanceType,
            @RequestParam("LaunchSpecification.NcaId") String ncaId,
            @RequestParam(value = "LaunchSpecification.Placement.AvailabilityZones",
                          required = false) String availabilityZones,
            @RequestParam("InstanceCount") int instanceCount,
            @RequestParam("LaunchSpecification.ContainerImage") String containerImage,
            @RequestParam(value = "LaunchSpecification.HelmChart",
                          required = false) String helmChart,
            @RequestParam("LaunchSpecification.Environment") String environment,
            @RequestParam(value = "LaunchSpecification.Models",
                          required = false) String models,
            @RequestParam(value = "LaunchSpecification.ArtifactUrl",
                          required = false) URI artifactUrl,
            @RequestParam("LaunchSpecification.CacheArtifacts") boolean cacheArtifacts,
            @RequestParam(value = "LaunchSpecification.CacheHandle",
                          required = false) String cacheHandle,
            @RequestParam(value = "LaunchSpecification.Configuration",
                          required = false) String configuration,
            @RequestParam(value = "LaunchSpecification.CacheSize",
                          required = false) Long cacheSize,
            @RequestParam("LaunchSpecification.DeploymentId") UUID deploymentId,
            @RequestParam("LaunchSpecification.GpuSpecificationId") UUID gpuSpecificationId,
            @RequestParam("FunctionDetails.FunctionId") UUID functionId,
            @RequestParam("FunctionDetails.FunctionVersionId") UUID functionVersionId,
            @RequestParam("FunctionDetails.OwnerNcaId") String ownerNcaId,
            @RequestParam("FunctionDetails.FunctionName") String functionName,
            @RequestParam(value = "LaunchSpecification.Clusters",
                          required = false) Set<String> clusters,
            @RequestParam(value = "LaunchSpecification.Regions",
                          required = false) Set<String> regions,
            @RequestParam(value = "LaunchSpecification.Attributes",
                          required = false) Set<String> attributes,
            @RequestParam("FunctionDetails.FunctionType") FunctionType functionType,
            @RequestParam(value = "LaunchSpecification.Telemetries",
                          required = false) String telemetries,
            @RequestParam(value = "LaunchSpecification.HelmValidationPolicy",
                          required = false) String helmValidationPolicy
    );

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
                String containerImage;
                String models;
                Placement placement;
                String gpu;
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
                    return isRunning() || isStarting();
                }

                public boolean isRunning() {
                    return "running".equals(name);
                }

                public boolean isStarting() {
                    return "starting".equals(name);
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
    void deleteInstances(
            @RequestParam("InstanceId") List<String> instanceIds
    );

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
            List<String> instanceIps;
        }
    }

    @GetExchange("/v1/si?Action=DescribeInstances")
    DescribeInstancesResponse describeInstances(
            @RequestParam("InstanceId") List<String> instanceIds
    );

    @GetExchange("/v1/si/accounts/{ncaId}/instanceTypes")
    Map<String, Set<InstanceTypeDetails>> getInstanceTypes(
            @PathVariable("ncaId") String ncaId,
            @RequestParam("instanceTypeUsage") InstanceUsageTypeEnum instanceUsage);

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

    @GetExchange("/v1/si/accounts/{ncaId}/clusters?includeAuthorizedClusters=true&includeGfnInAuthorizedClusters=true")
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

    @GetExchange("/v1/si/accounts/{ncaId}/workloads/{workloadId}/instances")
    Instances getInstancesByDeploymentId(
            @PathVariable("ncaId") String ncaId,
            @PathVariable("workloadId") UUID deploymentId,
            @RequestParam("IncludeTerminated") boolean includeTerminated,
            @RequestParam("UseConciseName") boolean useConciseName,
            @RequestParam("ExpiredAckedInstances") boolean expiredAckedInstances);

    @DeleteExchange("/v1/si/accounts/{ncaId}/instances/{instanceId}")
    void deleteInstance(
            @PathVariable("ncaId") String ncaId,
            @PathVariable("instanceId") String instanceId);

    @DeleteExchange("/v1/si/accounts/{ncaId}/workloads/{workloadId}")
    void deleteInstancesByDeploymentId(
            @PathVariable("ncaId") String ncaId,
            @PathVariable("workloadId") UUID deploymentId);

    @Value
    @Jacksonized
    @Builder
    class WorkerTokenIntrospectRequest {
        @JsonProperty("token")
        String token;
    }

    @Value
    @Jacksonized
    @Builder
    class WorkerTokenIntrospectResult {
        boolean active;
        @Nullable String sub;
        @Nullable String aud;
        @Nullable String iss;
        @JsonProperty("instance_id")
        @Nullable String instanceId;
        @JsonProperty("worker_id")
        @Nullable String workerId;
        @JsonProperty("token_type")
        @Nullable String tokenType;
        @Nullable String error;
    }

    @PostExchange(url = "/v1/workers/tokens/introspect",
                  accept = APPLICATION_JSON_VALUE,
                  contentType = APPLICATION_JSON_VALUE)
    WorkerTokenIntrospectResult introspectWorkerToken(
            @org.springframework.web.bind.annotation.RequestBody WorkerTokenIntrospectRequest request);
}
