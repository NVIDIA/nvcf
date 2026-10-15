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

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_AUTHORIZED_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.getClusterByGroupIdAndIdEntity;
import static com.nvidia.icms.util.TestUtil.getDummyClustersByAuthorizedAccountResp;
import static com.nvidia.icms.util.TestUtil.getDummyGpuV5;
import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import java.util.List;
import java.util.Set;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class WildCardAllowedClustersCacheServiceTest {

    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @InjectMocks
    private WildCardAllowedClustersCacheService wildCardAllowedClustersCacheService;

    @Test
    void getReadyClustersForWildCardNcaId_withValidCache_returnFromCache(){
        // Prepare
        ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                getDummyClustersByAuthorizedAccountResp(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                        DUMMY_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                                                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8,
                                                        "*");

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = getClusterByGroupIdAndIdEntity(
                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME, DUMMY_CLUSTER_GROUP_ID,
                DUMMY_BYOC_CLUSTER_GROUP_NAME,
                DUMMY_BYOC_NCA_ID, Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                            DUMMY_GPU_NAME)));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(NvcaClusterRepository.WILDCARD)).thenReturn(
                List.of(clustersByAuthorizedAccountsEntity));

        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                List.of(clusterByGroupIdAndIdEntity));
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());

        // Act
        Set<ClusterByGroupIdAndIdEntity> resp = wildCardAllowedClustersCacheService.getCachedReadyClusters();

        // Assert
        assertEquals(Set.of(clusterByGroupIdAndIdEntity), resp);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(NvcaClusterRepository.WILDCARD);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(telemetryEventClient).triggerEvent(Mockito.any());
    }

    @Test
    void getCachedReadyClusters_withValidCache_returnFromCache(){
        // Prepare
        ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                getDummyClustersByAuthorizedAccountResp(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                        DUMMY_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                                                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8,
                                                        "*");

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = getClusterByGroupIdAndIdEntity(
                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME, DUMMY_CLUSTER_GROUP_ID,
                DUMMY_BYOC_CLUSTER_GROUP_NAME,
                DUMMY_BYOC_NCA_ID, Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                        DUMMY_GPU_NAME)));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(NvcaClusterRepository.WILDCARD)).thenReturn(
                List.of(clustersByAuthorizedAccountsEntity));

        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                List.of(clusterByGroupIdAndIdEntity));

        when(icmsConfigurationProperties.getWildCardStaleCachedDataValidDurationInSec()).thenReturn(60L);

        // Act
        // Updating the cache
        wildCardAllowedClustersCacheService.refreshCache();
        Set<ClusterByGroupIdAndIdEntity> resp = wildCardAllowedClustersCacheService.getCachedReadyClusters();

        // Assert
        assertEquals(Set.of(clusterByGroupIdAndIdEntity), resp);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(NvcaClusterRepository.WILDCARD);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(telemetryEventClient, times(0)).triggerEvent(Mockito.any());
    }
}