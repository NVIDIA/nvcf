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

import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyByAccountOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyByAccountOwnerAndServiceRepository;
import com.nvidia.apikeys.vo.AccountKeysPageVo;
import com.nvidia.apikeys.vo.KeyByAccountOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyVo;
import java.nio.ByteBuffer;
import java.util.Base64;
import java.util.List;
import java.util.Optional;
import java.util.function.Function;
import lombok.RequiredArgsConstructor;
import org.springframework.data.cassandra.core.CassandraBatchOperations;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.WriteResult;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

/**
 * Account-scoped key persistence. Writes the hash lookup row in keys and the management row in
 * keys_by_account_owner_and_service in one logged batch.
 */
@Service
@RequiredArgsConstructor
public class AccountKeysDao {

    private final KeyByAccountOwnerAndServiceRepository repository;
    private final KeyModelConverter keyConverter;
    private final KeyByAccountOwnerAndServiceModelConverter accountKeyConverter;
    private final KeysDao keysDao;
    private final CassandraTemplate cassandraTemplate;

    public KeyByAccountOwnerAndServiceVo save(KeyVo key) {
        if (key.getNcaId() == null) {
            throw new IllegalArgumentException("nca_id is required for account-scoped keys");
        }

        KeyModel keyModel = keyConverter.voToModel(key);
        KeyByAccountOwnerAndServiceModel accountKeyModel =
                accountKeyConverter.voToModel(KeyByAccountOwnerAndServiceVo.from(key));

        WriteResult writeResult = cassandraTemplate.batchOps()
                .insert(List.of(keyModel))
                .insert(List.of(accountKeyModel))
                .execute();

        if (!writeResult.wasApplied()) {
            throw new CassandraException("Failed to write account key into db");
        }

        if (keysDao.getKeyByHash(key.getKeyHash()).isEmpty()) {
            throw new CassandraException("Failed to read saved key");
        }

        return get(key.getNcaId(), key.getOwnerType(), key.getOwnerId(),
                   key.getIssuerServiceId(), key.getKeyId())
                .orElseThrow(() -> new CassandraException("Failed to read saved account key"));
    }

    public Optional<KeyByAccountOwnerAndServiceVo> get(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        return repository.findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                        ncaId, ownerType, ownerId, issuerServiceId, keyId)
                .map(accountKeyConverter::modelToVo);
    }

    public List<KeyByAccountOwnerAndServiceVo> list(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        return repository.findByNcaIdAndOwnerTypeAndOwnerId(ncaId, ownerType, ownerId)
                .stream()
                .map(accountKeyConverter::modelToVo)
                .toList();
    }

    public List<KeyByAccountOwnerAndServiceVo> list(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        return repository.findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceId(
                        ncaId, ownerType, ownerId, issuerServiceId)
                .stream()
                .map(accountKeyConverter::modelToVo)
                .toList();
    }

    public AccountKeysPageVo listByAccount(String ncaId, int pageSize, String pagingState) {
        return page(pageable -> repository.findByNcaId(ncaId, pageable), pageSize, pagingState);
    }

    public AccountKeysPageVo listByAccountAndService(
            String ncaId, String issuerServiceId, int pageSize, String pagingState) {
        return page(pageable -> repository.findByNcaIdAndIssuerServiceId(
                ncaId, issuerServiceId, pageable), pageSize, pagingState);
    }

    public void delete(KeyByAccountOwnerAndServiceVo key) {
        KeyModel keyModel = KeyModel.builder()
                .keyHash(key.getKeyHash())
                .keyStatus(key.getKeyStatus())
                .build();

        KeyByAccountOwnerAndServiceModel accountKeyModel = KeyByAccountOwnerAndServiceModel.builder()
                .ncaId(key.getNcaId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(key.getIssuerServiceId())
                .keyId(key.getKeyId())
                .build();

        CassandraBatchOperations batchOperations = cassandraTemplate.batchOps()
                .delete(List.of(keyModel))
                .delete(List.of(accountKeyModel));

        if (!batchOperations.execute().wasApplied()) {
            throw new CassandraException("Failed to delete account key.");
        }
    }

    private AccountKeysPageVo page(
            Function<Pageable, Slice<KeyByAccountOwnerAndServiceModel>> query,
            int pageSize, String pagingState) {
        Slice<KeyByAccountOwnerAndServiceModel> slice = query.apply(pageRequest(pageSize,
                                                                                pagingState));
        List<KeyByAccountOwnerAndServiceVo> keys = slice.getContent().stream()
                .map(accountKeyConverter::modelToVo)
                .toList();
        return new AccountKeysPageVo(keys, nextPagingState(slice));
    }

    private static CassandraPageRequest pageRequest(int pageSize, String pagingState) {
        if (pagingState == null) {
            return CassandraPageRequest.first(pageSize);
        }
        ByteBuffer state = ByteBuffer.wrap(Base64.getUrlDecoder().decode(pagingState));
        return CassandraPageRequest.of(PageRequest.of(0, pageSize), state);
    }

    private static String nextPagingState(Slice<?> slice) {
        if (!slice.hasNext()) {
            return null;
        }
        ByteBuffer state = ((CassandraPageRequest) slice.nextPageable()).getPagingState();
        if (state == null) {
            return null;
        }
        byte[] bytes = new byte[state.remaining()];
        state.duplicate().get(bytes);
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
    }
}
