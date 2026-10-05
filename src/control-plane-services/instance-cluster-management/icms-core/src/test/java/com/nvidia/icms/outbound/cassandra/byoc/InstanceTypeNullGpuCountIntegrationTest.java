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
package com.nvidia.icms.outbound.cassandra.byoc;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

/**
 * Pins what ICMS reads for the {@code gpu_count} of a stored instance type, because CPU-only
 * support gives a count of 0 a meaning.
 *
 * <p>A null count cannot be read at all: the mapper fails while setting the primitive field. So a
 * stored null never turns into 0 or 1, and the only way a zero exists is an explicit 0, which is
 * what a CPU shape registers.
 */
class InstanceTypeNullGpuCountIntegrationTest extends IntegrationTest {

    @Autowired
    private ClusterRepository clusterRepository;

    private static String insertWithGpuCount(String clusterId, String gpuCountClause) {
        return "INSERT INTO test.cluster_by_cluster_id (cluster_id, gpus_v5) VALUES ('"
                + clusterId + "', { { name: 'A100', capacity: 1, instance_types: { "
                + "{ name: 'X.GPU.A100_1x', value: 'X.GPU.A100_1x', is_default: true, "
                + gpuCountClause + "cpu_cores: 8, system_memory: '32Gi', gpu_memory: '40Gi' } } } })";
    }

    private static Throwable rootCause(Throwable throwable) {
        Throwable cause = throwable;
        while (cause.getCause() != null && cause.getCause() != cause) {
            cause = cause.getCause();
        }
        return cause;
    }

    @Test
    void instanceTypeStoredWithoutGpuCount_cannotBeRead_soNullNeverMeansZeroOrOne() {
        // gpu_count is omitted from the user-defined type value, so the column is null.
        CQL_SESSION.execute(insertWithGpuCount("null-gpu-count-cluster", ""));

        Throwable thrown = assertThrows(RuntimeException.class,
                () -> clusterRepository.getClusterInfoByClusterId("null-gpu-count-cluster", false));

        assertInstanceOf(NullPointerException.class, rootCause(thrown));
    }

    @Test
    void instanceTypeStoredWithAnExplicitZero_readsBackAsZero() {
        CQL_SESSION.execute(insertWithGpuCount("zero-gpu-count-cluster", "gpu_count: 0, "));

        Optional<ClusterEntity> cluster =
                clusterRepository.getClusterInfoByClusterId("zero-gpu-count-cluster", false);

        assertTrue(cluster.isPresent());
        InstanceTypeV5Udt instanceType =
                cluster.get().getGpusV5().iterator().next().getInstanceTypes().iterator().next();
        assertEquals(0, instanceType.getGpuCount());
    }
}
