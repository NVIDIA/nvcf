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

class ClusterValidationTest {

    @Test
    void authorizedAccountsRejectOwnerAndMixedWildcard() {
        assertThrows(IcmsConflictException.class,
                () -> ClusterValidation.validateNcaIdDoNotBelongsToAuthorizedNcaIds(
                        Set.of("owner"), "owner"));
        assertThrows(IcmsConflictException.class,
                () -> ClusterValidation.validateAuthorizedNcaIdsForNewCluster(
                        Set.of("*", "other")));
        assertDoesNotThrow(() -> ClusterValidation.validateAuthorizedNcaIdsForNewCluster(
                Set.of("*")));
    }

    @Test
    void instanceTypesRejectChangedSpecs() {
        var existing = InstanceTypeV5Udt.builder().name("gpu-1").value("large")
                .description("large GPU").gpuMemory("80G").systemMemory("128G")
                .cpuCores(16).build();
        var changed = InstanceTypeV5Udt.builder().name("gpu-1").value("large")
                .description("large GPU").gpuMemory("80G").systemMemory("128G")
                .cpuCores(32).build();

        assertDoesNotThrow(() -> ClusterValidation.validateInstanceTypesV5(
                Set.of(existing), Set.of(existing), true, "gpu"));
        assertThrows(IcmsConflictException.class,
                () -> ClusterValidation.validateInstanceTypesV5(
                        Set.of(existing), Set.of(changed), true, "gpu"));
    }
}
