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
package com.nvidia.icms.inbound.rest.model.nvca;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Get Cluster Response")
@Builder
public class GetClusterResponse {

    @Schema(description = "Name of the cluster")
    String clusterName;

    @Schema(description = "Group to which the custer belongs")
    String clusterGroupName;

    @Schema(description = "Description of the cluster")
    String clusterDescription;

    @Schema(description = "NVIDIA Cloud Account ID")
    String ncaId;

    @Schema(description = "AuthorizedNcaIds for the cluster")
    Set<String> authorizedNCAIds;

    @Schema(description = "Cloud Provider for the cluster")
    ClusterProviderEnum cloudProvider;

    @Schema(description = "Region of the cluster")
    String region;

    @Schema(description = "Capabilities of the cluster")
    Set<String> capabilities;

    @Schema(description = "Attributes related to the cluster")
    Set<String> attributes;

    @Schema(description = "Custom attributes related to the cluster")
    Set<String> customAttributes;

    @Schema(description = "GPUs supported in cluster")
    @Nullable
    Set<GpuResponseSchema> gpus;

    @Schema(description = "Nvca version")
    String nvcaVersion;

    @Deprecated
    @Schema(description = "OAuth client ID used by cluster (deprecated, use oAuthClientId)")
    String ssaClientId;

    @JsonProperty("oAuthClientId")
    @Schema(description = "OAuth client ID used by cluster")
    String oAuthClientId;

    @Schema(description = "Cluster ID")
    String clusterId;

    @Schema(description = "Cluster Group ID")
    String clusterGroupId;

    @Schema(description = "Cluster status")
    String status;

    @Schema(description = "Cluster Source")
    String clusterSource;

    @Schema(description = "NVCA last connected timestamp")
    @Nullable
    Instant nvcaLastConnected;

    @Schema(description = "K8s version on cluster")
    @Nullable
    String k8sVersion;

    @Schema(description = "Gpu usage shared by cluster")
    @Nullable
    Map<String, GpuCapacity> gpuUsage;


    @Schema(description = "Sis Config")
    SisConfig sisConfig;

    @Schema(description = "Vault Config")
    VaultConfig vaultConfig;

    @Schema(description = "Image Credential Helper Config")
    @Nullable
    ImageCredentialHelper imageCredentialHelper;

    @Schema(description = "Cluster upgrade status")
    @Nullable
    String clusterUpgradeStatus;

    @Schema(description = "Cluster key Id")
    @Nullable
    String clusterKeyId;

    @Schema(description = "Advanced BYOC cluster configurations as key-value string map")
    @Nullable
    Map<String, String> clusterConfigurations;

    @Schema(description = "Advanced BYOC cluster configuration files as base64-encoded string map")
    @Nullable
    Map<String, String> clusterConfigurationFiles;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class GpuResponseSchema {

        @Valid
        @NotBlank
        @JsonProperty("name")
        @Schema(description = "Name of the gpu")
        String name;

        @JsonProperty("capacity")
        @Schema(description = "Total gpu capacity")
        int capacity;

        @JsonProperty("instanceTypes")
        @Schema(description = "List of instance types for that Gpu")
        Set<InstanceTypeResponseSchema> instanceTypes;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class InstanceTypeResponseSchema {

        @Schema(description = "Number of cpu cores for the instance type")
        int cpuCores;

        @Schema(description = "Memory of the System. e.g. 24G")
        String systemMemory;

        @Schema(description = "Memory of the GPU. e.g. 24G")
        String gpuMemory;

        @Schema(description = "Number of GPUs to be attached to the worker pod. " +
                "Supported values 1, 2, 4, 8 (Default value is 1)")
        int gpuCount = 1;

        @Valid
        @NotBlank(message = "Instance name must not blank")
        @Schema(description = "Name of the instance type")
        String name;

        @Schema(description = "Description of the instance type")
        String description;

        @JsonProperty("default")
        @Schema(description = "Indicates if its the default instance type for the GPU")
        Boolean isDefault;

        @Valid
        @NotBlank(message = "Instance value must not blank")
        @Schema(description = "Value of the instance type")
        String value;

        @Schema(description = "Architecture details of the CPU")
        String cpuArch;

        @Schema(description = "Operating system details")
        String os;

        @Schema(description = "GPU driver version")
        String driverVersion;

        @Schema(description = "The amount of available storage, e.g. 80G")
        String storage;
    }
}
