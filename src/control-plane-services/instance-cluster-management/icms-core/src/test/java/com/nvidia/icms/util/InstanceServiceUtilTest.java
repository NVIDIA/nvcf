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
package com.nvidia.icms.util;

import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceServiceUtilTest {

    @Test
    void getInstanceTypeInformation_success() {
        // Actual instance type
        InstanceServiceUtil.InstanceTypeInformation instanceInfo1 =
                InstanceServiceUtil.getInstanceTypeInformation("BM.GPU.A100-v2.8");

        Assertions.assertEquals(1, instanceInfo1.getGpuCount());
        Assertions.assertEquals("BM.GPU.A100-v2.8", instanceInfo1.getInstanceName());

        // Custom instance type
        InstanceServiceUtil.InstanceTypeInformation instanceInfo2 =
                InstanceServiceUtil.getInstanceTypeInformation("BM.GPU.A100-v2.8_8x");

        Assertions.assertEquals(8, instanceInfo2.getGpuCount());
        Assertions.assertEquals("BM.GPU.A100-v2.8", instanceInfo2.getInstanceName());

        // Custom instance type with spaces
        InstanceServiceUtil.InstanceTypeInformation instanceInfo3 =
                InstanceServiceUtil.getInstanceTypeInformation(" BM.GPU.A100-v2.8_8x ");

        Assertions.assertEquals(8, instanceInfo3.getGpuCount());
        Assertions.assertEquals("BM.GPU.A100-v2.8", instanceInfo3.getInstanceName());

        // Custom instance type without gpuCount
        // Consider GPU count as 1
        // Performance class as provided instance type
        InstanceServiceUtil.InstanceTypeInformation instanceInfo4 =
                InstanceServiceUtil.getInstanceTypeInformation("BM.GPU.A100-v2.8_dx");

        Assertions.assertEquals(1, instanceInfo4.getGpuCount());
        Assertions.assertEquals("BM.GPU.A100-v2.8_dx", instanceInfo4.getInstanceName());

        // Custom instance type with spaces
        InstanceServiceUtil.InstanceTypeInformation instanceInfo5 =
                InstanceServiceUtil.getInstanceTypeInformation(" B_M.GPU.A_10-0-v2.8_8x ");

        Assertions.assertEquals(8, instanceInfo5.getGpuCount());
        Assertions.assertEquals("B_M.GPU.A_10-0-v2.8", instanceInfo5.getInstanceName());
    }
}
