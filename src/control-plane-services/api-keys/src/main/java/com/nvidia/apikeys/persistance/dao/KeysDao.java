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

import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;
import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;

import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.nvidia.apikeys.converters.KeyOwnerVoBuilder;
import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeysByOwnerAndAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeysByOwnerAndAccountAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyByOwnerAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.KeysByAccountSliceVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.SavedKeyVo;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import java.time.Clock;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.function.Function;
import java.util.stream.Stream;
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
 * Key persistence. Every key has a hash lookup row in keys, read by introspection, and a
 * management row in an index table. Saves and deletes write both rows in one logged batch.
 *
 * <p>Legacy keys are indexed per user in keys_by_owner_and_service. Keys issued for a specific
 * account are indexed in keys_by_owner_and_account_and_service and use the account key methods.
 */
@Service
@RequiredArgsConstructor
public class KeysDao {

    private static final String MESG_INVALID_CURSOR = "Invalid cursor: '%s'";
    private static final String MESG_INVALID_LIMIT = "Invalid limit: '%d'";

    private final KeyRepository keyRepository;
    private final KeyByOwnerAndServiceRepository keyByOwnerAndServiceRepository;
    private final KeysByOwnerAndAccountAndServiceRepository keysByOwnerAndAccountAndServiceRepository;
    private final KeyModelConverter keyConverter;
    private final KeyOwnerVoBuilder keyOwnerVoBuilder;
    private final Clock clock;
    private final CassandraTemplate cassandraTemplate;


    public SavedKeyVo save(KeyVo key, KeyOwnerVo owner) {
        // write key record
        KeyModel keyModel = keyConverter.voToModel(key);

        // populate reverse lookup
        KeyByOwnerAndServiceVo keyByOwnerAndServiceVo = new KeyByOwnerAndServiceVo(key, owner);
        var keyByOwnerAndServiceModel = keyConverter.voToModel(
                keyByOwnerAndServiceVo);

        CassandraBatchOperations batchOperations = cassandraTemplate.batchOps()
                .insert(List.of(keyModel))
                .insert(List.of(keyByOwnerAndServiceModel));

        WriteResult writeResult = batchOperations.execute();

        if (!writeResult.wasApplied()) {
            throw new CassandraException("Failed to write key into db");
        }

        // read saved values back
        key = getKeyByHash(key.getKeyHash())
                .orElseThrow(() -> new CassandraException("Failed to read saved key"));

        keyByOwnerAndServiceVo = getKeyByOwnerAndServiceAndId(
                key.getOwnerType(), key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId())
                .orElseThrow(() -> new CassandraException("Failed to read saved key owner"));

        return SavedKeyVo.builder()
                .keyVo(key)
                .keyByOwnerAndServiceVo(keyByOwnerAndServiceVo)
                .build();
    }

    public Optional<KeyVo> getKeyByHash(String hash) {
        return keyRepository.findByKeyHash(hash)
                .map(this::modelToValidatedVo);
    }

    public Optional<KeyOwnerVo> getKeyOwner(KeyOwnerType type, String subject) {
        // need to be careful here when listing without service id to get key owner status
        // you're no guaranteed to have any keys so user may come back with all fields as null
        // except for partition key and static column values that will stick forever
        return keyByOwnerAndServiceRepository.findFirstByOwnerTypeAndOwnerId(type, subject)
                .map(keyOwnerVoBuilder::getKeyOwnerVoFromModel);
    }

    public Optional<KeyByOwnerAndServiceVo> getKeyByOwnerAndServiceAndId(
            KeyOwnerType type, String ownerId, String serviceId, String keyId) {
        return keyByOwnerAndServiceRepository.findByOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                        type, ownerId, serviceId, keyId)
                .map(this::modelToValidatedVo);
    }

    public KeyOwnerVo save(KeyOwnerVo vo) {
        ResultSet resultSet = keyByOwnerAndServiceRepository.updateKeyOwner(
                vo.getOwnerType(), vo.getOwnerId(),
                vo.getOwnerStatus(), vo.getOwnerStatusUpdatedAt());

        if (resultSet == null || !resultSet.wasApplied()) {
            throw new UnprocessableEntityException("failed to update key owner");
        }

        return getKeyOwner(vo.getOwnerType(), vo.getOwnerId())
                .orElseThrow(() -> new CassandraException("failed to read saved key owner"));
    }

    public void deleteKey(KeyByOwnerAndServiceVo key) {
        KeyModel keyModel = KeyModel.builder()
                .keyHash(key.getKeyHash())
                .keyStatus(key.getKeyStatus())
                .build();

        var keyByOwnerAndServiceModel = keyConverter.voToModel(key);

        CassandraBatchOperations batchOperations = cassandraTemplate.batchOps()
                .delete(List.of(keyModel))
                .delete(List.of(keyByOwnerAndServiceModel));

        WriteResult writeResult = batchOperations.execute();

        if (!writeResult.wasApplied()) {
            throw new CassandraException("Failed to delete key.");
        }
    }

    public List<KeyByOwnerAndServiceVo> list(
            KeyOwnerType ownerType, String ownerId, String serviceId) {
        return keyByOwnerAndServiceRepository
                .findByOwnerTypeAndOwnerIdAndIssuerServiceId(ownerType, ownerId, serviceId)
                .stream()
                .filter(Objects::nonNull)
                .map(this::modelToValidatedVo)
                .toList();
    }

    public List<KeyByOwnerAndServiceVo> list(KeyOwnerType ownerType, String ownerId) {
        return keyByOwnerAndServiceRepository
                .findByOwnerTypeAndOwnerId(ownerType, ownerId)
                .stream()
                .filter(Objects::nonNull)
                // we may get single record (user part, static columns) even when there are no keys
                // make sure only actual keys make it into response
                .filter(key -> key.getKeyId() != null)
                .map(this::modelToValidatedVo)
                .toList();
    }

    public KeysByOwnerAndAccountAndServiceVo saveAccountKey(KeyVo key) {
        if (key.getNcaId() == null) {
            throw new IllegalArgumentException("nca_id is required for account-scoped keys");
        }

        KeyModel keyModel = keyConverter.voToModel(key);
        KeysByOwnerAndAccountAndServiceModel accountKeyModel =
                keyConverter.voToModel(KeysByOwnerAndAccountAndServiceVo.from(key));

        WriteResult writeResult = cassandraTemplate.batchOps()
                .insert(List.of(keyModel))
                .insert(List.of(accountKeyModel))
                .execute();

        if (!writeResult.wasApplied()) {
            throw new CassandraException("Failed to write account key into db");
        }

        if (getKeyByHash(key.getKeyHash()).isEmpty()) {
            throw new CassandraException("Failed to read saved key");
        }

        return getAccountKey(key.getNcaId(), key.getOwnerType(), key.getOwnerId(),
                             key.getIssuerServiceId(), key.getKeyId())
                .orElseThrow(() -> new CassandraException("Failed to read saved account key"));
    }

    public Optional<KeysByOwnerAndAccountAndServiceVo> getAccountKey(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                        ncaId, ownerType, ownerId, issuerServiceId, keyId)
                .map(keyConverter::modelToVo);
    }

    public Stream<KeysByOwnerAndAccountAndServiceVo> listAccountKeys(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerId(ncaId, ownerType, ownerId)
                .map(keyConverter::modelToVo);
    }

    public Stream<KeysByOwnerAndAccountAndServiceVo> listAccountKeys(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceId(
                        ncaId, ownerType, ownerId, issuerServiceId)
                .map(keyConverter::modelToVo);
    }

    public KeysByAccountSliceVo listKeysByAccount(String ncaId, int limit, String cursor) {
        return sliceAccountKeys(
                pageable -> keysByOwnerAndAccountAndServiceRepository.findByNcaId(ncaId, pageable),
                limit, cursor);
    }

    public KeysByAccountSliceVo listKeysByAccountAndService(
            String ncaId, String issuerServiceId, int limit, String cursor) {
        return sliceAccountKeys(
                pageable -> keysByOwnerAndAccountAndServiceRepository.findByNcaIdAndIssuerServiceId(
                        ncaId, issuerServiceId, pageable),
                limit, cursor);
    }

    public void deleteAccountKey(KeysByOwnerAndAccountAndServiceVo key) {
        KeyModel keyModel = KeyModel.builder()
                .keyHash(key.getKeyHash())
                .keyStatus(key.getKeyStatus())
                .build();

        KeysByOwnerAndAccountAndServiceModel accountKeyModel = KeysByOwnerAndAccountAndServiceModel.builder()
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

    private KeysByAccountSliceVo sliceAccountKeys(
            Function<Pageable, Slice<KeysByOwnerAndAccountAndServiceModel>> query,
            int limit, String cursor) {
        if (limit < 1) {
            throw new IllegalArgumentException(MESG_INVALID_LIMIT.formatted(limit));
        }
        Slice<KeysByOwnerAndAccountAndServiceModel> pagedResult;
        try {
            var byteBuffer = cursor == null ? null : fromHexString(cursor);
            var pageRequest = CassandraPageRequest.of(PageRequest.of(0, limit), byteBuffer);
            pagedResult = query.apply(pageRequest);
        } catch (RuntimeException e) {
            if (cursor == null) {
                throw e;
            }
            throw new IllegalArgumentException(MESG_INVALID_CURSOR.formatted(cursor), e);
        }

        var keys = pagedResult.getContent().stream()
                .map(keyConverter::modelToVo)
                .toList();
        var builder = KeysByAccountSliceVo.builder().keys(keys);
        if (pagedResult.hasNext()) {
            var pagingState = ((CassandraPageRequest) pagedResult.getPageable()).getPagingState();
            builder.cursor(toHexString(pagingState));
            builder.limit(limit);
        }
        return builder.build();
    }

    private KeyVo modelToValidatedVo(KeyModel keyModel) {
        return keyConverter.modelToVo(keyModel);
    }

    private KeyByOwnerAndServiceVo modelToValidatedVo(KeyByOwnerAndServiceModel model) {
        return keyConverter.modelToVo(model);
    }
}
