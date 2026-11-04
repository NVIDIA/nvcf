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
package com.nvidia.icms.service.internal;

import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.getDummyInstancePlacement;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.util.TestUtil;
import java.util.Optional;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InternalInstanceServiceHelperTest {

    @Mock
    ByocService byocService;

    InternalInstanceServiceHelper internalInstanceServiceHelper;

    @BeforeEach
    void setUp() {
        // Configure a compute platform so placement defaults resolve to the
        // non-BYOC provider identity, preserving pre-genericization behaviour.
        internalInstanceServiceHelper = new InternalInstanceServiceHelper(
                byocService, ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void validateInstancePlacement_forNonByoc_returnsSuccess() {
        // Act
        InternalInstanceServiceHelper.InstancePlacementValidationResponse response =
                internalInstanceServiceHelper.validateInstancePlacement(
                        getDummyInstancePlacement(), DUMMY_CLUSTER_ID,
                        DUMMY_REQUEST_ID);

        // Assert
        assertEquals(ResourceProvider.OCI, response.getResourceProvider());
        assertEquals(CloudProvider.OCI, response.getCloudProvider());
        assertEquals(getDummyInstancePlacement().getAvailabilityZone(),
                     response.getInstancePlacement().getAvailabilityZone());

        verifyNoInteractions(byocService);
    }

    @Test
    void validateInstancePlacement_forByoc_returnsSuccess() {
        // Prepare
        when(byocService.getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID)).thenReturn(
                Optional.of(TestUtil.getDummyClusterEntity()));

        // Act
        InternalInstanceServiceHelper.InstancePlacementValidationResponse response =
                internalInstanceServiceHelper.validateInstancePlacement(
                        null, DUMMY_CLUSTER_ID,
                        DUMMY_REQUEST_ID);

        // Assert
        assertEquals(ResourceProvider.BYOC, response.getResourceProvider());
        assertEquals(CloudProvider.GDN, response.getCloudProvider());
        assertEquals("id", response.getInstancePlacement().getAvailabilityZone());

        verify(byocService).getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);
    }

    @Test
    void validateInstancePlacement_forByocAndClusterNotFound_throwsException() {
        // Prepare
        when(byocService.getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID)).thenReturn(
                Optional.empty());

        // Act
        IcmsNotFoundException icmsBadRequestException =
                Assertions.assertThrows(IcmsNotFoundException.class, () -> {
                    internalInstanceServiceHelper.validateInstancePlacement(
                            null, DUMMY_CLUSTER_ID,
                            DUMMY_REQUEST_ID);
                });

        // Assert
        assertEquals("Cloud not find any cluster with cluster_id clusterId",
                     icmsBadRequestException.getBody().getDetail());
        verify(byocService).getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);
    }
}