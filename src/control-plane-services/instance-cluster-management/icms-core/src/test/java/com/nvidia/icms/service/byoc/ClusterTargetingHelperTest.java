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

import static com.nvidia.icms.inbound.rest.controllers.account.AccountInfoControllerTest.getDummyClusterGroup;
import static com.nvidia.icms.service.byoc.ClusterTargetingHelper.isClusterHealthyAndCapacityAvailable;
import static com.nvidia.icms.util.TestUtil.DUMMY_AZURE_GPU_NAME;
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
import static com.nvidia.icms.util.TestUtil.toClusterByGroupIdAndIdEntity;
import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.util.TestUtil;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterTargetingHelperTest {

    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private CloudHealthRepository cloudHealthRepository;

    @Mock
    private WildCardAllowedClustersCacheService wildCardAllowedClustersCacheService;

    @InjectMocks
    private ClusterTargetingHelper clusterTargetingHelper;

    @Test
    void getReadyClusterEntitiesForNcaId_withValidInputs_returnsSuccess(){
        // Prepare

        String ncaId = DUMMY_BYOC_NCA_ID;

        ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                getDummyClustersByAuthorizedAccountResp(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                        DUMMY_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                                                        ncaId, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8,
                                                        DUMMY_BYOC_AUTHORIZED_NCA_ID);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = getClusterByGroupIdAndIdEntity(
                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME, DUMMY_CLUSTER_GROUP_ID,
                DUMMY_BYOC_CLUSTER_GROUP_NAME,
                ncaId, Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                     DUMMY_GPU_NAME)));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ncaId)).thenReturn(
                List.of(clustersByAuthorizedAccountsEntity));

        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                List.of(clusterByGroupIdAndIdEntity));

        // Act
        Set<ClusterByGroupIdAndIdEntity> resp = clusterTargetingHelper.getReadyClusterEntitiesForNcaId(
                ncaId);

        // Assert
        assertEquals(Set.of(clusterByGroupIdAndIdEntity), resp);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ncaId);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
    }

    @Test
    void isClusterHealthyAndCapacityAvailable_withValidInputs_returnsTrue(){
        // Prepare
        CloudHealthEntity cloudHealth = TestUtil.getDummyCloudHealthEntity(DUMMY_CLUSTER_ID,
                                                                           DUMMY_AZURE_GPU_NAME,
                                                                           CloudHealthStatus.HEALTHY,
                                                                           ResourceProvider.BYOC,
                                                                           10, 5, 5);

        // Act
        boolean resp = isClusterHealthyAndCapacityAvailable(cloudHealth, null, true);

        // Assert
        assertTrue(resp);
    }

    @Test
    void isClusterHealthyAndCapacityAvailable_witCapacityNotAvailable_returnsFalse(){
        // Prepare
        CloudHealthEntity cloudHealth = TestUtil.getDummyCloudHealthEntity(DUMMY_CLUSTER_ID,
                                                                           DUMMY_AZURE_GPU_NAME,
                                                                           CloudHealthStatus.HEALTHY,
                                                                           ResourceProvider.BYOC,
                                                                           0, 5, 5);

        // Act
        boolean resp = isClusterHealthyAndCapacityAvailable(cloudHealth, null, true);

        // Assert
        assertFalse(resp);
    }

    @Test
    void isClusterHealthyAndCapacityAvailable_witCloudUnhealthy_returnsFalse(){

        // Act
        // For unhealthy cloud, entry won't be present in map
        boolean resp = isClusterHealthyAndCapacityAvailable(null, null, true);

        // Assert
        assertFalse(resp);
    }

    @ParameterizedTest()
    @ValueSource(booleans = {true, false})
    void isClusterHealthyAndCapacityAvailable_withgpu_returnsTrue(boolean withCurrentGpu){
        // Prepare
        CloudHealthEntity cloudHealth = TestUtil.getDummyCloudHealthEntity(DUMMY_CLUSTER_ID,
                                                                           DUMMY_AZURE_GPU_NAME,
                                                                           CloudHealthStatus.HEALTHY,
                                                                           ResourceProvider.BYOC,
                                                                           10, 5, 5);
        // Act
        boolean resp = isClusterHealthyAndCapacityAvailable(cloudHealth, Set.of(withCurrentGpu ? DUMMY_AZURE_GPU_NAME : DUMMY_GPU_NAME), true);

        // Assert
        assertEquals(withCurrentGpu, resp);
    }

    @ParameterizedTest()
    @ValueSource(booleans = {true, false})
    void isClusterHealthyAndCapacityAvailable_withCheckCapacityFalse_returnsTrue(boolean withGpu){
        // Test scenario where checkCapacity=false should skip capacity checks
        CloudHealthEntity cloudHealth = TestUtil.getDummyCloudHealthEntity(DUMMY_CLUSTER_ID,
                                                                           DUMMY_AZURE_GPU_NAME,
                                                                           CloudHealthStatus.HEALTHY,
                                                                           ResourceProvider.BYOC,
                                                                           0, 5, 5); // Zero capacity

        boolean resp = isClusterHealthyAndCapacityAvailable(cloudHealth, withGpu ? Set.of(DUMMY_AZURE_GPU_NAME) :  null, false);

        assertTrue(resp); // Should return true despite zero capacity
    }

    @ParameterizedTest()
    @ValueSource(booleans = {true, false})
    void isClusterHealthyAndCapacityAvailable_withCheckCapacity_WrongGpu_ReturnFalse(boolean checkCapacity){
        // Test scenario where checkCapacity=false should skip capacity checks
        CloudHealthEntity cloudHealth = TestUtil.getDummyCloudHealthEntity(DUMMY_CLUSTER_ID,
                                                                           DUMMY_AZURE_GPU_NAME,
                                                                           CloudHealthStatus.HEALTHY,
                                                                           ResourceProvider.BYOC,
                                                                           0, 5, 5); // Zero capacity

        boolean resp = isClusterHealthyAndCapacityAvailable(cloudHealth, Set.of(DUMMY_GPU_NAME), checkCapacity);

        assertFalse(resp); // Should return true despite zero capacity
    }

}