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
package com.nvidia.icms.service.byoc;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import java.util.Set;
import org.junit.jupiter.api.Test;

class ClusterRegistrationServiceInstanceTypeComparisonTest {

    private static InstanceTypeV5Udt gpuType() {
        return InstanceTypeV5Udt.builder().name("NCP.GPU.H100_1x").value("NCP.GPU.H100_1x")
                .description("one h100").gpuMemory("80Gi").systemMemory("64Gi").cpuCores(8)
                .gpuCount(1).isDefault(true).build();
    }

    private static InstanceTypeV5Udt cpuShape() {
        return InstanceTypeV5Udt.builder().name("NCP.CPU.4c16g").value("NCP.CPU.4c16g")
                .systemMemory("16Gi").cpuCores(4).gpuCount(0).isDefault(true).build();
    }

    private static void compare(
            InstanceTypeV5Udt expected, InstanceTypeV5Udt actual, String gpuName) {
        ClusterRegistrationService.validateInstanceTypesV5(
                Set.of(expected), Set.of(actual), true, gpuName);
    }

    @Test
    void cpuShapeWithoutDescriptionOrGpuMemory_comparesWithoutANullPointerException() {
        // A CPU shape has no GPU, so gpuMemory and often description are null. They used to be
        // dereferenced, which was an NPE and an HTTP 500 when a second cluster shared the name.
        assertDoesNotThrow(() -> compare(cpuShape(), cpuShape(), "CPU"));
    }

    @Test
    void cpuShapesThatDifferInGpuCountOrMachineFieldsConflict() {
        InstanceTypeV5Udt differentStorage = cpuShape();
        differentStorage.setStorage("500Gi");
        InstanceTypeV5Udt differentArch = cpuShape();
        differentArch.setCpuArch("arm64");
        InstanceTypeV5Udt differentOs = cpuShape();
        differentOs.setOs("windows");
        InstanceTypeV5Udt differentNodeType = cpuShape();
        differentNodeType.setNodeType("MULTI");
        InstanceTypeV5Udt differentGpuCount = cpuShape();
        differentGpuCount.setGpuCount(5);

        for (InstanceTypeV5Udt other : new InstanceTypeV5Udt[] {
                differentStorage, differentArch, differentOs, differentNodeType, differentGpuCount}) {
            assertThrows(IcmsConflictException.class, () -> compare(cpuShape(), other, "CPU"));
        }
    }

    @Test
    void gpuTypesKeepTheExistingComparison() {
        // The extra fields are compared only for the reserved name, so GPU group membership is
        // decided exactly as before.
        InstanceTypeV5Udt differentStorage = gpuType();
        differentStorage.setStorage("500Gi");
        InstanceTypeV5Udt differentArch = gpuType();
        differentArch.setCpuArch("arm64");

        assertDoesNotThrow(() -> compare(gpuType(), differentStorage, "H100"));
        assertDoesNotThrow(() -> compare(gpuType(), differentArch, "H100"));
        // The existing fields still conflict.
        InstanceTypeV5Udt differentMemory = gpuType();
        differentMemory.setGpuMemory("40Gi");
        assertThrows(IcmsConflictException.class, () -> compare(gpuType(), differentMemory, "H100"));
    }

    @Test
    void gpuNamedCpuKeepsTheExistingComparison() {
        // Feature off, or a collision: a real GPU registered under the reserved name is compared
        // exactly as before. Only entries that are CPU shapes get the stricter comparison.
        InstanceTypeV5Udt differentStorage = gpuType();
        differentStorage.setStorage("500Gi");

        assertDoesNotThrow(() -> compare(gpuType(), differentStorage, "CPU"));
    }
}
