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

package com.nvidia.icms.service.workers;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentCaptor.forClass;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.workers.WorkerAuth;
import com.nvidia.icms.inbound.rest.model.workers.WorkerIdentifier;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.workers.WorkerIdentifierRepository;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class WorkerIdentifierServiceTest {

    private static final String CLUSTER_ID = "cl-abc123";
    private static final String INSTANCE_ID = "inst-789";
    private static final String NAMESPACE = "inst-789";
    private static final String SA_UID = "7c1f0b2a-9d4e-4a8c-bb21-0f3e5d6a1c22";
    private static final String POD_UID = "f1e2d3c4-5b6a-7980-1234-aabbccddeeff";

    @Mock
    private WorkerIdentifierRepository workerIdentifierRepository;

    @Mock
    private InstanceV2Repository instanceV2Repository;

    private WorkerIdentifierService service;

    @BeforeEach
    void setUp() {
        service = new WorkerIdentifierService(workerIdentifierRepository, instanceV2Repository);
    }

    @Test
    void storeWorkerIdentifiers_savesRecordWithTtlAndAllFields() {
        WorkerAuth auth = validAuth();

        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, auth);

        ArgumentCaptor<WorkerIdentifierRecord> captor = forClass(WorkerIdentifierRecord.class);
        verify(workerIdentifierRepository).saveWithTtl(captor.capture(),
                eq(WorkerIdentifierService.IDENTIFIER_TTL));
        var saved = captor.getValue();

        assertEquals(CLUSTER_ID, saved.getKey().getClusterId());
        assertEquals(INSTANCE_ID, saved.getKey().getInstanceId());
        assertEquals(auth.getSub(), saved.getSub());
        assertEquals(NAMESPACE, saved.getNamespace());
        assertEquals(SA_UID, saved.getSaUid());
        assertEquals(1, saved.getIdentifiers().size());
        assertEquals("utils", saved.getIdentifiers().get(0).getName());
        assertEquals(POD_UID, saved.getIdentifiers().get(0).getUid());
    }

    @Test
    void storeWorkerIdentifiers_subNotMatchingNamespace_rejected() {
        WorkerAuth auth = validAuth();
        auth.setSub("system:serviceaccount:other-ns:nvcf-worker");

        assertThrows(IcmsBadRequestException.class,
                () -> service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, auth));
        verify(workerIdentifierRepository, never()).saveWithTtl(any(), any());
    }

    @Test
    void storeWorkerIdentifiers_subNotWorkerServiceAccount_rejected() {
        WorkerAuth auth = validAuth();
        auth.setSub("system:serviceaccount:" + NAMESPACE + ":default");

        assertThrows(IcmsBadRequestException.class,
                () -> service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, auth));
        verify(workerIdentifierRepository, never()).saveWithTtl(any(), any());
    }

    @Test
    void storeWorkerIdentifiers_secondCallReplacesFirst() {
        WorkerAuth first = validAuth();
        WorkerAuth second = validAuth();
        second.setWorkerIdentifiers(List.of(
                WorkerIdentifier.builder().name("utils-1").uid(POD_UID).build(),
                WorkerIdentifier.builder().name("utils-2").uid(SA_UID).build()));

        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, first);
        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, second);

        ArgumentCaptor<WorkerIdentifierRecord> captor = forClass(WorkerIdentifierRecord.class);
        verify(workerIdentifierRepository, times(2)).saveWithTtl(captor.capture(), any());
        assertEquals(2, captor.getAllValues().get(1).getIdentifiers().size());
    }

    @Test
    void findWorkerIdentifiersBySaUid_delegatesToRepo() {
        var record = WorkerIdentifierRecord.builder().sub("sub").build();
        when(workerIdentifierRepository.findByClusterIdAndSaUid(CLUSTER_ID, SA_UID))
                .thenReturn(Optional.of(record));

        assertTrue(service.findWorkerIdentifiersBySaUid(CLUSTER_ID, SA_UID).isPresent());
    }

    @Test
    void findWorkerIdentifiers_notFound_returnsEmpty() {
        when(workerIdentifierRepository.findByClusterIdAndInstanceId(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.empty());

        assertFalse(service.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID).isPresent());
    }

    @Test
    void deleteWorkerIdentifiers_delegatesToRepo() {
        service.deleteWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID);

        verify(workerIdentifierRepository).deleteByClusterIdAndInstanceId(CLUSTER_ID, INSTANCE_ID);
    }

    @Test
    void deleteForRequestQuietly_deletesEveryInstanceOnItsOwnCluster() {
        var a = new InstanceV2Entity();
        a.setInstanceId("i-a");
        a.setZone("cl-a");
        var b = new InstanceV2Entity();
        b.setInstanceId("i-b");
        b.setZone("cl-b");
        when(instanceV2Repository.findInstancesByRequestId("req")).thenReturn(List.of(a, b));

        service.deleteForRequestQuietly("req");

        verify(workerIdentifierRepository).deleteByClusterIdAndInstanceId("cl-a", "i-a");
        verify(workerIdentifierRepository).deleteByClusterIdAndInstanceId("cl-b", "i-b");
    }

    @Test
    void deleteForRequestQuietly_swallowsRepositoryErrors() {
        when(instanceV2Repository.findInstancesByRequestId("req"))
                .thenThrow(new IcmsInternalServerException("db down"));

        service.deleteForRequestQuietly("req");
    }

    @Test
    void deleteForClusterQuietly_dropsPartitionAndSwallowsErrors() {
        service.deleteForClusterQuietly(CLUSTER_ID);
        verify(workerIdentifierRepository).deleteByClusterId(CLUSTER_ID);

        doThrow(new IcmsInternalServerException("db down"))
                .when(workerIdentifierRepository).deleteByClusterId("cl-x");
        service.deleteForClusterQuietly("cl-x");
    }

    @Test
    void deleteForInstanceQuietly_ignoresNulls() {
        service.deleteForInstanceQuietly(null, INSTANCE_ID);
        service.deleteForInstanceQuietly(CLUSTER_ID, null);

        verify(workerIdentifierRepository, never()).deleteByClusterIdAndInstanceId(any(), any());
    }

    private static WorkerAuth validAuth() {
        return WorkerAuth.builder()
                .sub(WorkerAuth.expectedSubject(NAMESPACE))
                .namespace(NAMESPACE)
                .saUid(SA_UID)
                .workerIdentifiers(List.of(
                        WorkerIdentifier.builder().name("utils").uid(POD_UID).build()))
                .build();
    }
}
