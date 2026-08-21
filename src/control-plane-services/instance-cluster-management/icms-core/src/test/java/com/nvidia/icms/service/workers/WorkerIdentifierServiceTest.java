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
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentCaptor.forClass;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.workers.WorkerAuth;
import com.nvidia.icms.inbound.rest.model.workers.WorkerIdentifier;
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

    @Mock
    private WorkerIdentifierRepository workerIdentifierRepository;

    private WorkerIdentifierService service;

    @BeforeEach
    void setUp() {
        service = new WorkerIdentifierService(workerIdentifierRepository);
    }

    @Test
    void storeWorkerIdentifiers_savesRecordWithCorrectFields() {
        WorkerAuth auth = WorkerAuth.builder()
                .sub("system:serviceaccount:nvcf-backend:nvcf-worker-" + INSTANCE_ID)
                .saUid("sa-uid-1234")
                .workerIdentifiers(List.of(
                        WorkerIdentifier.builder().name("pod-0").uid("pod-uid-0").build()))
                .build();

        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, auth);

        ArgumentCaptor<WorkerIdentifierRecord> captor = forClass(WorkerIdentifierRecord.class);
        verify(workerIdentifierRepository).save(captor.capture());
        var saved = captor.getValue();

        assertEquals(CLUSTER_ID, saved.getKey().getClusterId());
        assertEquals(INSTANCE_ID, saved.getKey().getInstanceId());
        assertEquals(auth.getSub(), saved.getSub());
        assertEquals("sa-uid-1234", saved.getSaUid());
        assertEquals(1, saved.getIdentifiers().size());
        assertEquals("pod-0", saved.getIdentifiers().get(0).getName());
        assertEquals("pod-uid-0", saved.getIdentifiers().get(0).getUid());
    }

    @Test
    void storeWorkerIdentifiers_secondCallReplacesFirst() {
        WorkerAuth firstAuth = WorkerAuth.builder()
                .sub("sub1")
                .workerIdentifiers(List.of(WorkerIdentifier.builder().name("pod-0").uid("uid-0").build()))
                .build();
        WorkerAuth secondAuth = WorkerAuth.builder()
                .sub("sub1")
                .workerIdentifiers(List.of(
                        WorkerIdentifier.builder().name("pod-1").uid("uid-1").build(),
                        WorkerIdentifier.builder().name("pod-2").uid("uid-2").build()))
                .build();

        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, firstAuth);
        service.storeWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID, secondAuth);

        ArgumentCaptor<WorkerIdentifierRecord> captor = forClass(WorkerIdentifierRecord.class);
        verify(workerIdentifierRepository, org.mockito.Mockito.times(2)).save(captor.capture());

        var lastSaved = captor.getAllValues().get(1);
        assertEquals(2, lastSaved.getIdentifiers().size());
        assertEquals("pod-1", lastSaved.getIdentifiers().get(0).getName());
    }

    @Test
    void findWorkerIdentifiers_delegatesToRepo() {
        var record = WorkerIdentifierRecord.builder().sub("sub").build();
        when(workerIdentifierRepository.findByClusterIdAndInstanceId(CLUSTER_ID, INSTANCE_ID))
                .thenReturn(Optional.of(record));

        var result = service.findWorkerIdentifiers(CLUSTER_ID, INSTANCE_ID);

        assertTrue(result.isPresent());
        assertEquals("sub", result.get().getSub());
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
}
