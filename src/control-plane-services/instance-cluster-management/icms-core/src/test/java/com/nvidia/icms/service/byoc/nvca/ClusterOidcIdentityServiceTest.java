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
package com.nvidia.icms.service.byoc.nvca;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterOidcIdentityServiceTest {

    private static final String CLUSTER_ID = "cluster-a";
    private static final String OTHER_CLUSTER_ID = "cluster-b";
    private static final String JWKS = "{\"keys\":[]}";
    private static final String ISSUER = "https://k8s.example.com/oidc";
    private static final String FINGERPRINT = "fingerprint";

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    private ClusterOidcIdentityService service;

    @BeforeEach
    void setUp() {
        service = new ClusterOidcIdentityService(clusterRepository, nvcaClusterRepository);
    }

    @Test
    void findByFingerprint_returnsClusterWithMatchingFingerprint() {
        ClusterEntity expected = ClusterEntity.builder()
                .clusterId(CLUSTER_ID)
                .jwksFingerprint(FINGERPRINT)
                .build();
        when(clusterRepository.getAllClusters()).thenReturn(List.of(
                ClusterEntity.builder().clusterId(OTHER_CLUSTER_ID).jwksFingerprint("other").build(),
                expected));

        Optional<ClusterEntity> result = service.findByFingerprint(FINGERPRINT);

        assertTrue(result.isPresent());
        assertEquals(CLUSTER_ID, result.get().getClusterId());
    }

    @Test
    void validateFingerprintAvailable_rejectsDifferentCluster() {
        when(clusterRepository.getAllClusters()).thenReturn(List.of(
                ClusterEntity.builder()
                        .clusterId(OTHER_CLUSTER_ID)
                        .jwksFingerprint(FINGERPRINT)
                        .build()));

        IcmsConflictException ex = assertThrows(IcmsConflictException.class,
                () -> service.validateFingerprintAvailable(FINGERPRINT, CLUSTER_ID));

        assertTrue(ex.getMessage().contains(OTHER_CLUSTER_ID));
    }

    @Test
    void updateOidcIdentity_updatesOnlyClusterEntity() {
        ClusterEntity cluster = ClusterEntity.builder().clusterId(CLUSTER_ID).build();
        when(clusterRepository.getClusterInfoByClusterId(CLUSTER_ID, false))
                .thenReturn(Optional.of(cluster));

        service.updateOidcIdentity(CLUSTER_ID, JWKS, ISSUER, FINGERPRINT);

        assertEquals(JWKS, cluster.getJwks());
        assertEquals(ISSUER, cluster.getOidcIssuer());
        assertEquals(FINGERPRINT, cluster.getJwksFingerprint());
        verify(nvcaClusterRepository).updateClusterEntity(cluster);
    }

    @Test
    void updateOidcIdentity_unknownClusterThrowsNotFound() {
        when(clusterRepository.getClusterInfoByClusterId(CLUSTER_ID, false))
                .thenReturn(Optional.empty());

        assertThrows(IcmsNotFoundException.class,
                () -> service.updateOidcIdentity(CLUSTER_ID, JWKS, ISSUER, FINGERPRINT));
    }
}
