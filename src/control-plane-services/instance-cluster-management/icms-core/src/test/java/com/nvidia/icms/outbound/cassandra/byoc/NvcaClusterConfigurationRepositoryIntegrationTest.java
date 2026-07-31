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
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterConfigurationByClusterIdEntity;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class NvcaClusterConfigurationRepositoryIntegrationTest extends IntegrationTest {

    @Autowired
    private NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    @Test
    void save_find_delete_configuration_success() {
        // Given
        String clusterId = "it-test-cluster";
        Map<String, String> cfg = Map.of("priorityClassName", "system-cluster-critical");
        Map<String, String> files = Map.of("baseNetworkFile", "YmFzZTY0");

        // When - save
        nvcaClusterConfigurationRepository.saveOrUpdateConfiguration(clusterId, cfg, files);

        // Then - find
        var found = nvcaClusterConfigurationRepository.findByClusterId(clusterId);
        assertTrue(found.isPresent());
        ClusterConfigurationByClusterIdEntity entity = found.get();
        assertEquals("system-cluster-critical", entity.getClusterConfigurations().get("priorityClassName"));
        assertEquals("YmFzZTY0", entity.getClusterConfigurationFiles().get("baseNetworkFile"));

        // When - delete
        nvcaClusterConfigurationRepository.deleteByClusterId(clusterId);

        // Then - absent
        var afterDelete = nvcaClusterConfigurationRepository.findByClusterId(clusterId);
        assertFalse(afterDelete.isPresent());
    }
}


