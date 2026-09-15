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
package com.nvidia.icms.inbound.rest.model.account;

import io.swagger.v3.oas.annotations.media.Schema;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;
import java.util.Comparator;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@Builder
@AllArgsConstructor
@Schema(description = "Instance type availability response")
public class InstanceTypeAvailabilityResponse {

    public InstanceTypeAvailabilityResponse() {
        gpus = new TreeSet<>(Comparator.comparing(Gpu::getGpuName));
    }

    @Schema(description = "List of GPUs with their instance types and availability")
    private Set<Gpu> gpus;

    public Gpu findOrCreateGpu(String gpuName) {
        if (gpus == null) {
            gpus = new TreeSet<>(Comparator.comparing(Gpu::getGpuName));
        }
        Gpu gpu = gpus.stream().filter(r -> r.gpuName.equals(gpuName)).findFirst().orElse(null);
        if (gpu == null) {
            gpu = Gpu.builder()
                    .gpuName(gpuName)
                    .instanceTypes(new TreeSet<>(Comparator.comparing(InstanceType::getInstanceName)))
                    .build();
            gpus.add(gpu);
        }
        return gpu;
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class Gpu {

        @Schema(description = "Name of the GPU")
        private String gpuName;

        @Schema(description = "List of instance types available for this GPU")
        private Set<InstanceType> instanceTypes;

        //Due to big number of parameters, other fields for new records will be set caller
        public InstanceType findOrCreateInstanceType(String instanceName) {
            if (instanceTypes == null) {
                instanceTypes = new TreeSet<>(Comparator.comparing(InstanceType::getInstanceName));
            }
            InstanceType instanceType = instanceTypes.stream().filter(r -> r.instanceName.equals(instanceName)).findFirst().orElse(null);
            if (instanceType == null) {
                instanceType = InstanceType.builder()
                        .instanceName(instanceName)
                        .regions(new TreeSet<>(Comparator.comparing(Region::getRegionName)))
                        .build();
                instanceTypes.add(instanceType);
            }
            return instanceType;
        }

    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class InstanceType {

        @Schema(description = "Name of the instance type")
        private String instanceName;

        @Schema(description = "Value of the instance type")
        private String value;

        @Schema(description = "Description of the instance type")
        private String description;

        @Schema(description = "CPU architecture")
        private String cpuArch;

        @Schema(description = "Number of CPU cores")
        private int cpuCores;

        @Schema(description = "System memory size")
        private String systemMemory;

        @Schema(description = "GPU memory size")
        private String gpuMemory;

        @Schema(description = "Number of GPUs")
        private int gpuCount;

        @Schema(description = "Operating system")
        private String os;

        @Schema(description = "Driver version")
        private String driverVersion;

        @Schema(description = "Storage configuration")
        private String storage;

        @Schema(description = "Type of node (SINGLE or MULTI)")
        private NodeType nodeType;

        @Schema(description = "Indicates if its the default instance type for the GPU in it's cluster")
        private boolean defaultable;

        @Schema(description = "List of regions where this instance type is available")
        private Set<Region> regions;

        public Region findOrCreateRegion(String regionName) {
            if (regions == null) {
                regions = new TreeSet<>(Comparator.comparing(Region::getRegionName));
            }
            Region region = regions.stream().filter(r -> r.regionName.equals(regionName)).findFirst().orElse(null);
            if (region == null) {
                region = Region.builder()
                        .regionName(regionName)
                        .clusters(new TreeSet<>(Comparator.comparing(Cluster::getClusterId)))
                        .build();
                regions.add(region);
            }
            return region;
        }
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class Region {

        @Schema(description = "Name of the region")
        private String regionName;

        @Schema(description = "List of clusters in this region")
        private Set<Cluster> clusters;

        public Cluster findOrCreateCluster(String clusterId) {
            if (clusters == null) {
                clusters = new TreeSet<>(Comparator.comparing(Cluster::getClusterId));
            }
            Cluster cluster = clusters.stream().filter(r -> r.clusterId.equals(clusterId)).findFirst().orElse(null);
            if (cluster == null) {
                cluster = Cluster.builder()
                        .clusterId(clusterId)
                        .build();
                clusters.add(cluster);
            }
            return cluster;
        }
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class Cluster {

        @Schema(description = "Unique identifier of the cluster")
        private String clusterId;

        @Schema(description = "Name of the cluster")
        private String clusterName;

        @Schema(description = "Cloud provider name")
        private String cloudProvider;

        @Schema(description = "Group the cluster belongs to")
        private String clusterGroup;

        @Schema(description = "Whether this is the default instance type")
        private Boolean isDefaultInstanceType;

        @Schema(description = "Maximum available capacity per gpu, instance type, region and cluster")
        private int maxClusterAvailableCapacity;

        @Schema(description = "List of cluster attributes")
        private Set<String> attributes;
    }

    public enum NodeType {
        SINGLE,
        MULTI
    }
}
