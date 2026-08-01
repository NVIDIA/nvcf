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
package com.nvidia.icms.inbound.rest.converters;

import com.nvidia.icms.inbound.rest.model.byoc.Gpu;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceType;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV4Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV3Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import jakarta.validation.constraints.NotNull;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

@Component
@Slf4j
public class NvcaRequestSchemaToUdtConverter {

    private NvcaRequestSchemaToUdtConverter() {

    }

    // Convert to GpuUdt

    public static GpuUdt toGpuUdt(@NotNull Gpu gpu) {
        return GpuUdt.builder()
                .name(gpu.getName())
                .instanceTypes(toInstanceTypeUdts(gpu.getInstanceTypes()))
                .build();
    }

    public static Set<GpuUdt> toGpuUdts(Set<Gpu> gpus) {
        if (gpus == null) {
            return new HashSet<>();
        }

        Set<GpuUdt> result = new HashSet<>();
        gpus.forEach(r -> result.add(toGpuUdt(r)));

        return result;
    }

    // Convert to InstanceTypeUdt
    public static InstanceTypeUdt toInstanceTypeUdt(@NotNull InstanceType instanceType) {
        return InstanceTypeUdt.builder()
                .cpuCores(instanceType.getCpuCores())
                .systemMemory(instanceType.getSystemMemory())
                .gpuMemory(instanceType.getGpuMemory())
                .gpuCount(instanceType.getGpuCount())
                .name(instanceType.getName())
                .description(instanceType.getDescription())
                .isDefault(instanceType.getIsDefault())
                .value(instanceType.getValue())
                .build();
    }

    public static Set<InstanceTypeUdt> toInstanceTypeUdts(Set<InstanceType> instanceTypes) {
        if (instanceTypes == null) {
            return new HashSet<>();
        }

        Set<InstanceTypeUdt> result = new HashSet<>();
        instanceTypes.forEach(r -> result.add(toInstanceTypeUdt(r)));

        return result;
    }

    // Convert GpuRequestSchema to GpuV4Udt

    public static GpuV4Udt toGpuV4Udt(@NotNull GpuRequestSchema gpuRequestSchema) {
        return GpuV4Udt.builder()
                .name(gpuRequestSchema.getName())
                .capacity(gpuRequestSchema.getCapacity())
                .instanceTypes(toInstanceTypeV3Udts(gpuRequestSchema.getInstanceTypes()))
                .build();
    }

    public static Set<GpuV4Udt> toGpuV4Udts(Set<GpuRequestSchema> gpuRequestSchemas) {
        if (gpuRequestSchemas == null) {
            return new HashSet<>();
        }

        Set<GpuV4Udt> result = new HashSet<>();
        gpuRequestSchemas.forEach(r -> result.add(toGpuV4Udt(r)));

        return result;
    }

    // Convert InstanceTypeRequestSchema to InstanceTypeV3Udt

    public static InstanceTypeV3Udt toInstanceTypeV3Udt(@NotNull InstanceTypeRequestSchema instanceTypeRequestSchema) {
        return InstanceTypeV3Udt.builder()
                .cpuCores(instanceTypeRequestSchema.getCpuCores())
                .systemMemory(instanceTypeRequestSchema.getSystemMemory())
                .gpuMemory(instanceTypeRequestSchema.getGpuMemory())
                .gpuCount(instanceTypeRequestSchema.getGpuCount())
                .name(instanceTypeRequestSchema.getName())
                .description(instanceTypeRequestSchema.getDescription())
                .isDefault(instanceTypeRequestSchema.getIsDefault())
                .value(instanceTypeRequestSchema.getValue())
                .cpuArch(instanceTypeRequestSchema.getCpuArch())
                .os(instanceTypeRequestSchema.getOs())
                .driverVersion(instanceTypeRequestSchema.getDriverVersion())
                .storage(instanceTypeRequestSchema.getStorage())
                .build();
    }

    public static Set<InstanceTypeV3Udt> toInstanceTypeV3Udts(Set<InstanceTypeRequestSchema> instanceTypeRequestSchemas) {
        if (instanceTypeRequestSchemas == null) {
            return new HashSet<>();
        }

        Set<InstanceTypeV3Udt> result = new HashSet<>();
        instanceTypeRequestSchemas.forEach(r -> result.add(toInstanceTypeV3Udt(r)));

        return result;
    }

    // Convert GpuRequestSchema to GpuV5Udt

    public static GpuV5Udt toGpuV5Udt(@NotNull GpuRequestSchema gpuRequestSchema) {
        return GpuV5Udt.builder()
                .name(gpuRequestSchema.getName())
                .capacity(gpuRequestSchema.getCapacity())
                .instanceTypes(toInstanceTypeV5Udts(gpuRequestSchema.getInstanceTypes()))
                .build();
    }

    public static Set<GpuV5Udt> toGpuV5Udts(Set<GpuRequestSchema> gpuRequestSchemas) {
        if (gpuRequestSchemas == null) {
            return new HashSet<>();
        }

        Set<GpuV5Udt> result = new HashSet<>();
        gpuRequestSchemas.forEach(r -> result.add(toGpuV5Udt(r)));

        return result;
    }

    // Convert InstanceTypeRequestSchema to InstanceTypeV5Udt

    private static InstanceTypeV5Udt toInstanceTypeV5Udt(@NotNull InstanceTypeRequestSchema instanceTypeRequestSchema) {
        return InstanceTypeV5Udt.builder()
                .cpuCores(instanceTypeRequestSchema.getCpuCores())
                .systemMemory(instanceTypeRequestSchema.getSystemMemory())
                .gpuMemory(instanceTypeRequestSchema.getGpuMemory())
                .gpuCount(instanceTypeRequestSchema.getGpuCount())
                .name(instanceTypeRequestSchema.getName())
                .description(instanceTypeRequestSchema.getDescription())
                .isDefault(instanceTypeRequestSchema.getIsDefault())
                .value(instanceTypeRequestSchema.getValue())
                .cpuArch(instanceTypeRequestSchema.getCpuArch())
                .os(instanceTypeRequestSchema.getOs())
                .driverVersion(instanceTypeRequestSchema.getDriverVersion())
                .storage(instanceTypeRequestSchema.getStorage())
                .nodeType(instanceTypeRequestSchema.getNodeType().toString())
                .build();
    }

    public static Set<InstanceTypeV5Udt> toInstanceTypeV5Udts(Set<InstanceTypeRequestSchema> instanceTypeRequestSchemas) {
        if (instanceTypeRequestSchemas == null) {
            return new HashSet<>();
        }

        Set<InstanceTypeV5Udt> result = new HashSet<>();
        instanceTypeRequestSchemas.forEach(r -> result.add(toInstanceTypeV5Udt(r)));

        return result;
    }
}
