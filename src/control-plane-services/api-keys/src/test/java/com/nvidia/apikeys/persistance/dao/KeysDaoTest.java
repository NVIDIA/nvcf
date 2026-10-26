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

package com.nvidia.apikeys.persistance.dao;

import static com.nvidia.apikeys.TestData.API_KEY_HASH_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_MODEL_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static com.nvidia.apikeys.vo.KeyStatus.ACTIVE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.inOrder;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.config.exceptions.CassandraUnavailableException;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyByOwnerAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.SavedKeyVo;
import java.time.Clock;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Answers;
import org.mockito.InOrder;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.CassandraWriteTimeoutException;
import org.springframework.data.cassandra.core.CassandraBatchOperations;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.DeleteOptions;
import org.springframework.data.cassandra.core.InsertOptions;
import org.springframework.data.cassandra.core.WriteResult;


@ExtendWith(MockitoExtension.class)
class KeysDaoTest {

    private static final long NOW_MICROS = 1_791_965_640_123_456L;
    private static final InsertOptions DIRECT_INSERT_OPTIONS =
            InsertOptions.builder().timestamp(NOW_MICROS).idempotent(true).build();
    private static final DeleteOptions DIRECT_DELETE_OPTIONS =
            DeleteOptions.builder().timestamp(NOW_MICROS).idempotent(true).build();

    @Mock
    private KeyModel modelMock;
    @Mock
    private KeyRepository keyRepositoryMock;
    @Mock
    private KeyByOwnerAndServiceRepository keyByOwnerAndServiceRepositoryMock;
    @Mock
    private KeyModelConverter converterMock;
    @Mock
    private KeyByOwnerAndServiceModelConverter keyByOwnerAndServiceConverterMock;
    @Mock
    private CassandraTemplate cassandraTemplateMock;
    @Mock
    private CassandraBatchOperations cassandraBatchOperationsMock;
    @Mock
    private WriteResult writeResultMock;
    @Mock
    private Clock clockMock;
    @Mock(answer = Answers.RETURNS_DEEP_STUBS)
    private CqlSession sessionMock;

    @InjectMocks
    private KeysDao dao;

    private final KeyModel expectedDeletedKeyModel = KeyModel.builder()
            .keyHash(API_KEY_HASH_1)
            .keyStatus(ACTIVE)
            .build();

    @Test
    void save() {
        mockSaveBatch();
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(true);
        mockReadBack();

        assertThat(dao.save(KEY_VO_1, KEY_OWNER_VO_1)).isEqualTo(expectedSavedKey());
        verify(cassandraTemplateMock, never()).insert(any(), any(InsertOptions.class));
    }

    @Test
    void create_doesNotReadBack() {
        mockSaveBatch();
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(true);

        dao.create(KEY_VO_1, KEY_OWNER_VO_1);

        verify(cassandraBatchOperationsMock).execute();
        verifyNoInteractions(keyRepositoryMock, keyByOwnerAndServiceRepositoryMock);
    }

    @Test
    void save_throwsIfFailedToWrite() {
        mockSaveBatch();
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.save(KEY_VO_1, KEY_OWNER_VO_1),
                "Failed to write key into db");
    }

    @Test
    void save_writesRowsDirectlyWhenBatchlogWriteTimesOut() {
        mockSaveBatch();
        when(cassandraBatchOperationsMock.execute()).thenThrow(batchlogTimeout());
        mockReadBack();

        assertThat(dao.save(KEY_VO_1, KEY_OWNER_VO_1)).isEqualTo(expectedSavedKey());

        InOrder inOrder = inOrder(cassandraTemplateMock);
        inOrder.verify(cassandraTemplateMock).insert(
                KEY_BY_OWNER_AND_SERVICE_MODEL_1, DIRECT_INSERT_OPTIONS);
        inOrder.verify(cassandraTemplateMock).insert(modelMock, DIRECT_INSERT_OPTIONS);
    }

    @Test
    void save_throwsUnavailableIfDirectWriteFails() {
        mockSaveBatch();
        when(cassandraBatchOperationsMock.execute()).thenThrow(batchlogTimeout());
        when(cassandraTemplateMock.insert(eq(KEY_BY_OWNER_AND_SERVICE_MODEL_1),
                                          any(InsertOptions.class)))
                .thenThrow(writeTimeout("SIMPLE"));

        assertThrowsExceptionWithDetails(
                CassandraUnavailableException.class, () -> dao.save(KEY_VO_1, KEY_OWNER_VO_1),
                "Key storage is temporarily unavailable, the outcome of the write is unknown");
        verify(cassandraTemplateMock, never()).insert(eq(modelMock), any(InsertOptions.class));
    }

    @Test
    void getKeyByHash() {
        when(keyRepositoryMock.findByKeyHash(API_KEY_HASH_1)).thenReturn(Optional.of(modelMock));
        when(converterMock.modelToVo(modelMock)).thenReturn(KEY_VO_1);

        assertThat(dao.getKeyByHash(API_KEY_HASH_1)).isEqualTo(Optional.of(KEY_VO_1));
    }

    @Test
    void deleteKey() {
        mockDeleteBatch();
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(true);

        dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1);

        verify(cassandraBatchOperationsMock).delete(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1));
        verify(cassandraBatchOperationsMock).delete(List.of(expectedDeletedKeyModel));
        verify(cassandraBatchOperationsMock).execute();
        verify(cassandraTemplateMock, never()).delete(any(), any(DeleteOptions.class));
    }

    @Test
    void deleteKey_throwsIfFailsToExecute() {
        mockDeleteBatch();
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1),
                "Failed to delete key.");
    }

    @Test
    void deleteKey_deletesRowsDirectlyWhenBatchlogWriteTimesOut() {
        mockDeleteBatch();
        when(cassandraBatchOperationsMock.execute()).thenThrow(batchlogTimeout());

        dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1);

        InOrder inOrder = inOrder(cassandraTemplateMock);
        inOrder.verify(cassandraTemplateMock).delete(
                expectedDeletedKeyModel, DIRECT_DELETE_OPTIONS);
        inOrder.verify(cassandraTemplateMock).delete(
                KEY_BY_OWNER_AND_SERVICE_MODEL_1, DIRECT_DELETE_OPTIONS);
    }

    @Test
    void deleteKey_throwsUnavailableIfDirectDeleteFails() {
        mockDeleteBatch();
        when(cassandraBatchOperationsMock.execute()).thenThrow(batchlogTimeout());
        when(cassandraTemplateMock.delete(eq(expectedDeletedKeyModel), any(DeleteOptions.class)))
                .thenThrow(writeTimeout("SIMPLE"));

        assertThrowsExceptionWithDetails(
                CassandraUnavailableException.class,
                () -> dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1),
                "Key storage is temporarily unavailable, the outcome of the write is unknown");
    }

    private void mockBatch() {
        when(sessionMock.getContext().getTimestampGenerator().next()).thenReturn(NOW_MICROS);
        when(cassandraTemplateMock.batchOps()).thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.withTimestamp(NOW_MICROS))
                .thenReturn(cassandraBatchOperationsMock);
    }

    private void mockSaveBatch() {
        mockBatch();
        when(converterMock.voToModel(KEY_VO_1)).thenReturn(modelMock);
        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);
        when(cassandraBatchOperationsMock.insert(List.of(modelMock)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.insert(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
    }

    private void mockDeleteBatch() {
        mockBatch();
        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);
        when(cassandraBatchOperationsMock.delete(List.of(expectedDeletedKeyModel)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.delete(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
    }

    private void mockReadBack() {
        when(keyRepositoryMock.findByKeyHash(API_KEY_HASH_1)).thenReturn(Optional.of(modelMock));
        when(converterMock.modelToVo(modelMock)).thenReturn(KEY_VO_1);
        when(keyByOwnerAndServiceRepositoryMock.findByOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1));
        when(keyByOwnerAndServiceConverterMock.modelToVo(KEY_BY_OWNER_AND_SERVICE_MODEL_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_VO_1);
    }

    private static SavedKeyVo expectedSavedKey() {
        return SavedKeyVo.builder()
                .keyByOwnerAndServiceVo(KEY_BY_OWNER_AND_SERVICE_VO_1)
                .keyVo(KEY_VO_1)
                .build();
    }

    private static CassandraWriteTimeoutException batchlogTimeout() {
        return writeTimeout("BATCH_LOG");
    }

    private static CassandraWriteTimeoutException writeTimeout(String writeType) {
        return new CassandraWriteTimeoutException(writeType, "Cassandra timeout during "
                + writeType + " write query at consistency TWO", null);
    }
}
