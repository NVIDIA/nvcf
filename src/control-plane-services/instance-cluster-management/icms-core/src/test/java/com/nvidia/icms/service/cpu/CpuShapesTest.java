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
package com.nvidia.icms.service.cpu;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class CpuShapesTest {

    @ParameterizedTest
    @ValueSource(strings = {"CPU", "cpu", "Cpu"})
    void isReservedGpuName_matchesAnyLetterCase(String name) {
        assertTrue(CpuShapes.isReservedGpuName(name));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"H100", "CPUS", "NCP.CPU.4c16g", " CPU"})
    void isReservedGpuName_rejectsOtherNames(String name) {
        assertFalse(CpuShapes.isReservedGpuName(name));
    }

    @Test
    void isCanonicalGpuName_requiresExactSpelling() {
        assertTrue(CpuShapes.isCanonicalGpuName("CPU"));
        assertFalse(CpuShapes.isCanonicalGpuName("cpu"));
        assertFalse(CpuShapes.isCanonicalGpuName(null));
    }

    @ParameterizedTest
    @ValueSource(strings = {"NCP.CPU.4c16g", "ON-PREM.CPU.8c32g", "ncp.cpu.small", "A.cPu.B"})
    void hasShapeName_acceptsProviderCpuShape(String name) {
        assertTrue(CpuShapes.hasShapeName(name));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {
            "NCP.GPU.H100_1x", "NCP.CPU", "CPU.4c16g", "NCP.CPU.4c16g.x", "NCP..4c16g",
            "NCP.CPU.", ".CPU.4c16g", "NCP.CPU2.4c16g", "H100"})
    void hasShapeName_rejectsAnythingElse(String name) {
        assertFalse(CpuShapes.hasShapeName(name));
    }

    @Test
    void isCpuShapeEntry_needsReservedNameShapeNameAndNoGpu() {
        assertTrue(CpuShapes.isCpuShapeEntry("CPU", "NCP.CPU.4c16g", 0));
        assertTrue(CpuShapes.isCpuShapeEntry("cpu", "NCP.CPU.4c16g", 0));
        // A GPU entry is never a CPU shape, whatever it is called.
        assertFalse(CpuShapes.isCpuShapeEntry("H100", "NCP.CPU.4c16g", 0));
        assertFalse(CpuShapes.isCpuShapeEntry("CPU", "NCP.GPU.H100_1x", 0));
        assertFalse(CpuShapes.isCpuShapeEntry("CPU", "NCP.CPU.4c16g", 1));
    }

    @Test
    void isValidShape_requiresNameValueNoGpuCoresAndMemory() {
        assertTrue(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 1, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "other", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.GPU.H100_1x", "NCP.CPU.4c16g", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 0, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, " "));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, null));
    }

    @Test
    void isValidShape_requiresNameAndValueToBeTheSameShape() {
        // Two different valid shape names are still a mismatch NVCA never sends.
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.8c32g", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "ncp.cpu.4c16g", 0, 4, "16Gi"));
    }

    @Test
    void isValidShape_requiresAValidLabelValueName() {
        // The name is the value of the node label NVCA selects the CPU node group by.
        // A label value must start and end with a letter or digit. A dash inside it is fine.
        assertFalse(CpuShapes.isValidShape("NCP.CPU.bad-", "NCP.CPU.bad-", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.bad_", "NCP.CPU.bad_", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.bad.", "NCP.CPU.bad.", 0, 4, "16Gi"));
        assertTrue(CpuShapes.isValidShape("NCP.CPU.-bad", "NCP.CPU.-bad", 0, 4, "16Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4 c", "NCP.CPU.4 c", 0, 4, "16Gi"));
        String tooLong = "NCP.CPU." + "x".repeat(60);
        assertFalse(CpuShapes.isValidShape(tooLong, tooLong, 0, 4, "16Gi"));
        assertTrue(CpuShapes.isValidShape("NCP.CPU.4c_16g-1", "NCP.CPU.4c_16g-1", 0, 4, "16Gi"));
    }

    @Test
    void isPositiveQuantity_acceptsTheFormsNvcaParses() {
        for (String ok : new String[] {"16Gi", "16G", "80G", "500m", "1.5Gi", "100", "2e3", "+4Ki", ".5", "1E"}) {
            assertTrue(CpuShapes.isPositiveQuantity(ok), ok);
        }
    }

    @Test
    void isPositiveQuantity_rejectsWhatNvcaCannotParseOrIsNotPositive() {
        for (String bad : new String[] {
            null, "", " ", "lots", "16 Gi", "16GB", "-4Gi", "0", "0Gi", "0.0", "Gi", "1..5", "1Gi2", "16gi"}) {
            assertFalse(CpuShapes.isPositiveQuantity(bad), String.valueOf(bad));
        }
    }

    @Test
    void isValidShape_withStorageRequiresAPositiveStorageQuantity() {
        assertTrue(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi", "50Gi"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi", null));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi", " "));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi", "0"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "16Gi", "lots"));
        assertFalse(CpuShapes.isValidShape("NCP.CPU.4c16g", "NCP.CPU.4c16g", 0, 4, "nope", "50Gi"));
    }
}
