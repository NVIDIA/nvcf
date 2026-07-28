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
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyByOwnerAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.SavedKeyVo;
import java.time.Clock;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.core.CassandraBatchOperations;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.WriteResult;


@ExtendWith(MockitoExtension.class)
class KeysDaoTest {

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

    @InjectMocks
    private KeysDao dao;


    @Test
    void save() {
        // mock conversion to model
        when(converterMock.voToModel(KEY_VO_1)).thenReturn(modelMock);
        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);

        // mock successful batch save
        when(cassandraTemplateMock.batchOps()).thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.insert(List.of(modelMock)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.insert(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(true);

        // mock read back key
        when(keyRepositoryMock.findByKeyHash(API_KEY_HASH_1)).thenReturn(Optional.of(modelMock));
        when(converterMock.modelToVo(modelMock)).thenReturn(KEY_VO_1);

        // mock read back key by owner, service, id
        when(keyByOwnerAndServiceRepositoryMock.findByOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1));
        when(keyByOwnerAndServiceConverterMock.modelToVo(KEY_BY_OWNER_AND_SERVICE_MODEL_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_VO_1);

        SavedKeyVo expectedValue = SavedKeyVo.builder()
                .keyByOwnerAndServiceVo(KEY_BY_OWNER_AND_SERVICE_VO_1)
                .keyVo(KEY_VO_1)
                .build();

        assertThat(dao.save(KEY_VO_1, KEY_OWNER_VO_1)).isEqualTo(expectedValue);
    }

    @Test
    void save_throwsIfFailedToWrite() {
        // mock conversion to model
        when(converterMock.voToModel(KEY_VO_1)).thenReturn(modelMock);
        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);

        // mock successful batch save
        when(cassandraTemplateMock.batchOps()).thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.insert(List.of(modelMock)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.insert(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.save(KEY_VO_1, KEY_OWNER_VO_1),
                "Failed to write key into db");
    }

    @Test
    void getKeyByHash() {
        when(keyRepositoryMock.findByKeyHash(API_KEY_HASH_1)).thenReturn(Optional.of(modelMock));
        when(converterMock.modelToVo(modelMock)).thenReturn(KEY_VO_1);

        assertThat(dao.getKeyByHash(API_KEY_HASH_1)).isEqualTo(Optional.of(KEY_VO_1));
    }

    @Test
    void deleteKey() {
        KeyModel expectedKeyModel = KeyModel.builder()
                .keyHash(API_KEY_HASH_1)
                .keyStatus(ACTIVE)
                .build();

        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);

        when(cassandraTemplateMock.batchOps()).thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.delete(List.of(expectedKeyModel)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.delete(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(true);

        dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1);

        verify(cassandraBatchOperationsMock).delete(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1));
        verify(cassandraBatchOperationsMock).delete(List.of(expectedKeyModel));
        verify(cassandraBatchOperationsMock).execute();
    }

    @Test
    void deleteKey_throwsIfFailsToExecute() {
        KeyModel expectedKeyModel = KeyModel.builder()
                .keyHash(API_KEY_HASH_1)
                .keyStatus(ACTIVE)
                .build();

        when(keyByOwnerAndServiceConverterMock.voToModel(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_MODEL_1);

        when(cassandraTemplateMock.batchOps()).thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.delete(List.of(expectedKeyModel)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.delete(List.of(KEY_BY_OWNER_AND_SERVICE_MODEL_1)))
                .thenReturn(cassandraBatchOperationsMock);
        when(cassandraBatchOperationsMock.execute()).thenReturn(writeResultMock);
        when(writeResultMock.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1),
                "Failed to delete key.");
    }
}
