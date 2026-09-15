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
package com.nvidia.icms.service.scheduled;

import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

@Slf4j
class GpuV5PopulationTaskTest extends IntegrationTest {

    @Autowired
    private ClusterRepository clusterRepository;

    @Autowired
    private GpusV5PopulationTask task;


    @Test
    void update_gpuV5() {
        // prepare

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        String clusterId = clusterEntity.getClusterId();
        clusterEntity.setGpusV5(null);
        clusterRepository.saveClusterInfo(clusterEntity);
        Optional<ClusterEntity> beforeUpdateClusterEntity = clusterRepository.getClusterInfoByClusterId(
                clusterId, false);
        Assertions.assertTrue(beforeUpdateClusterEntity.isPresent());
        Assertions.assertTrue(beforeUpdateClusterEntity.get().getGpusV5().isEmpty());

        // execute
        task.updateClusters();

        // validate
        Optional<ClusterEntity> updatedClusterEntity = clusterRepository.getClusterInfoByClusterId(
                clusterId, false);
        Assertions.assertTrue(updatedClusterEntity.isPresent());
        Assertions.assertNotNull(updatedClusterEntity.get().getGpusV5());
        log.info("after gpu {}", updatedClusterEntity.get().getGpusV5());
    }
}
