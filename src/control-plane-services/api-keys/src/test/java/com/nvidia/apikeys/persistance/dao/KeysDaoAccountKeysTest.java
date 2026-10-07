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

import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyByAccountAndOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyByAccountAndOwnerAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.KeyByAccountAndOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.ArgumentCaptor;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.core.CassandraBatchOperations;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.WriteResult;
import org.springframework.data.domain.Pageable;

@ExtendWith(MockitoExtension.class)
class KeysDaoAccountKeysTest {

    private static final KeyVo KEY = KEY_VO_1.toBuilder().ncaId("nca-1").build();
    private static final KeyByAccountAndOwnerAndServiceVo ACCOUNT_KEY =
            KeyByAccountAndOwnerAndServiceVo.from(KEY);

    @Mock
    private KeyByAccountAndOwnerAndServiceRepository repository;
    @Mock
    private KeyModelConverter keyConverter;
    @Mock
    private KeyRepository keyRepository;
    @Mock
    private CassandraTemplate cassandraTemplate;
    @Mock
    private CassandraBatchOperations batchOperations;
    @Mock
    private WriteResult writeResult;
    @Mock
    private KeyModel keyModel;
    @Mock
    private KeyByAccountAndOwnerAndServiceModel accountKeyModel;

    @InjectMocks
    private KeysDao dao;

    @Test
    void saveThrowsWhenBatchIsNotApplied() {
        mockInsertBatch();
        when(writeResult.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.saveAccountKey(KEY),
                "Failed to write account key into db");
        verify(keyRepository, never()).findByKeyHash(any());
    }

    @Test
    void saveThrowsWhenHashRowCannotBeReadBack() {
        mockInsertBatch();
        when(writeResult.wasApplied()).thenReturn(true);
        when(keyRepository.findByKeyHash(KEY.getKeyHash())).thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.saveAccountKey(KEY),
                "Failed to read saved key");
    }

    @Test
    void saveThrowsWhenAccountRowCannotBeReadBack() {
        mockInsertBatch();
        when(writeResult.wasApplied()).thenReturn(true);
        when(keyRepository.findByKeyHash(KEY.getKeyHash())).thenReturn(Optional.of(keyModel));
        when(keyConverter.modelToVo(keyModel)).thenReturn(KEY);
        when(repository.findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                KEY.getNcaId(), KEY.getOwnerType(), KEY.getOwnerId(), KEY.getIssuerServiceId(),
                KEY.getKeyId()))
                .thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.saveAccountKey(KEY),
                "Failed to read saved account key");
    }

    @Test
    void saveWritesBothRowsInOneBatch() {
        mockInsertBatch();
        when(writeResult.wasApplied()).thenReturn(true);
        when(keyRepository.findByKeyHash(KEY.getKeyHash())).thenReturn(Optional.of(keyModel));
        when(keyConverter.modelToVo(keyModel)).thenReturn(KEY);
        when(repository.findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                KEY.getNcaId(), KEY.getOwnerType(), KEY.getOwnerId(), KEY.getIssuerServiceId(),
                KEY.getKeyId()))
                .thenReturn(Optional.of(accountKeyModel));
        when(keyConverter.modelToVo(accountKeyModel)).thenReturn(ACCOUNT_KEY);

        assertThat(dao.saveAccountKey(KEY)).isEqualTo(ACCOUNT_KEY);
        verify(batchOperations).insert(List.of(keyModel));
        verify(batchOperations).insert(List.of(accountKeyModel));
        verify(batchOperations).execute();
    }

    @Test
    void deleteUsesPrimaryKeysOnly() {
        mockBatch();
        when(batchOperations.delete(anyList())).thenReturn(batchOperations);
        when(writeResult.wasApplied()).thenReturn(true);

        dao.deleteAccountKey(ACCOUNT_KEY);

        @SuppressWarnings("unchecked")
        ArgumentCaptor<List<?>> deleted = ArgumentCaptor.forClass(List.class);
        verify(batchOperations, times(2)).delete(deleted.capture());
        KeyModel hashRow = (KeyModel) deleted.getAllValues().get(0).get(0);
        KeyByAccountAndOwnerAndServiceModel accountRow =
                (KeyByAccountAndOwnerAndServiceModel) deleted.getAllValues().get(1).get(0);
        assertThat(hashRow.getKeyHash()).isEqualTo(KEY.getKeyHash());
        assertThat(hashRow.getKeyDetails()).isNull();
        assertThat(accountRow.getNcaId()).isEqualTo("nca-1");
        assertThat(accountRow.getKeyId()).isEqualTo(KEY.getKeyId());
        assertThat(accountRow.getKeyDetails()).isNull();
        verifyNoInteractions(keyConverter);
    }

    @Test
    void deleteThrowsWhenBatchIsNotApplied() {
        mockBatch();
        when(batchOperations.delete(anyList())).thenReturn(batchOperations);
        when(writeResult.wasApplied()).thenReturn(false);

        assertThrowsExceptionWithDetails(
                CassandraException.class, () -> dao.deleteAccountKey(ACCOUNT_KEY),
                "Failed to delete account key.");
    }

    private void mockBatch() {
        when(cassandraTemplate.batchOps()).thenReturn(batchOperations);
        when(batchOperations.execute()).thenReturn(writeResult);
    }

    private void mockInsertBatch() {
        mockBatch();
        when(keyConverter.voToModel(KEY)).thenReturn(keyModel);
        when(keyConverter.voToModel(ACCOUNT_KEY)).thenReturn(accountKeyModel);
        when(batchOperations.insert(anyList())).thenReturn(batchOperations);
    }

    @Test
    void queryFailureWithoutCursorIsNotReportedAsBadCursor() {
        var failure = new IllegalStateException("cassandra unavailable");
        when(repository.findByNcaId(eq("nca-1"), any(Pageable.class))).thenThrow(failure);

        assertThatThrownBy(() -> dao.listKeysByAccount("nca-1", 10, null)).isSameAs(failure);
    }

    @Test
    void queryFailureWithCursorIsReportedAsBadCursor() {
        when(repository.findByNcaIdAndIssuerServiceId(eq("nca-1"), eq("service-a"),
                                                      any(Pageable.class)))
                .thenThrow(new IllegalStateException("bad paging state"));

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> dao.listKeysByAccountAndService("nca-1", "service-a", 10, "0x00"),
                "Invalid cursor: '0x00'");
    }

    @ParameterizedTest
    @ValueSource(ints = {0, -1})
    void limitBelowOneIsBadRequestEvenWithCursor(int limit) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> dao.listKeysByAccount("nca-1", limit, null),
                "Invalid limit: '" + limit + "'");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> dao.listKeysByAccountAndService("nca-1", "service-a", limit, "0x00"),
                "Invalid limit: '" + limit + "'");
        verifyNoInteractions(repository);
    }
}
