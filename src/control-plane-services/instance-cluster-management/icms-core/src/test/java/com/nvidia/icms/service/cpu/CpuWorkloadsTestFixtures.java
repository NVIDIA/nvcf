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

import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import com.nvidia.icms.configuration.cpu.CpuWorkloadsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.List;
import java.util.function.Consumer;

/**
 * Builds real {@link CpuWorkloadsService} instances in each state for tests, so tests exercise the
 * actual rules instead of a mock that returns null.
 */
public final class CpuWorkloadsTestFixtures {

    private CpuWorkloadsTestFixtures() {
    }

    /** Switch off, the default. */
    public static CpuWorkloadsService disabled() {
        return service(false, List.of());
    }

    /** Switch on and no cluster holds a GPU under the reserved name. */
    public static CpuWorkloadsService ready() {
        return service(true, List.of());
    }

    /** Switch on and the given stored clusters are what the collision guard reads. */
    public static CpuWorkloadsService enabledWithClusters(List<ClusterEntity> storedClusters) {
        return service(true, storedClusters);
    }

    private static CpuWorkloadsService service(boolean enabled, List<ClusterEntity> clusters) {
        CpuWorkloadsConfigurationProperties properties = new CpuWorkloadsConfigurationProperties();
        properties.setEnabled(enabled);
        ClusterRepository clusterRepository = mock(ClusterRepository.class);
        // Lenient: a disabled service never reads clusters, and tests using Mockito strict stubs
        // must not fail because of that.
        lenient().doAnswer(invocation -> {
            Consumer<List<ClusterEntity>> consumer = invocation.getArgument(1);
            consumer.accept(clusters);
            return null;
        }).when(clusterRepository).forEachClusterPage(anyInt(), any());
        return new CpuWorkloadsService(properties, clusterRepository);
    }
}
