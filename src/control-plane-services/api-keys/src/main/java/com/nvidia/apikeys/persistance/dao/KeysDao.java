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

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.nvidia.apikeys.converters.KeyOwnerVoBuilder;
import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyByOwnerAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.SavedKeyVo;
import com.nvidia.boot.exceptions.ServiceUnavailableException;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import java.time.Clock;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.dao.DataAccessResourceFailureException;
import org.springframework.dao.TransientDataAccessException;
import org.springframework.data.cassandra.core.CassandraBatchOperations;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.DeleteOptions;
import org.springframework.data.cassandra.core.InsertOptions;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class KeysDao {

    private final KeyRepository keyRepository;
    private final KeyByOwnerAndServiceRepository keyByOwnerAndServiceRepository;
    private final KeyModelConverter keyConverter;
    private final KeyByOwnerAndServiceModelConverter keyByOwnerAndServiceConverter;
    private final KeyOwnerVoBuilder keyOwnerVoBuilder;
    private final Clock clock;
    private final CassandraTemplate cassandraTemplate;
    private final CqlSession session;


    /**
     * Writes a new key without reading it back. A failed read after a successful write would
     * report a persisted key as not created.
     */
    public void create(KeyVo key, KeyOwnerVo owner) {
        write(key, owner);
    }

    public SavedKeyVo save(KeyVo key, KeyOwnerVo owner) {
        write(key, owner);

        // read saved values back
        key = getKeyByHash(key.getKeyHash())
                .orElseThrow(() -> new CassandraException("Failed to read saved key"));

        var keyByOwnerAndServiceVo = getKeyByOwnerAndServiceAndId(
                key.getOwnerType(), key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId())
                .orElseThrow(() -> new CassandraException("Failed to read saved key owner"));

        return SavedKeyVo.builder()
                .keyVo(key)
                .keyByOwnerAndServiceVo(keyByOwnerAndServiceVo)
                .build();
    }

    private void write(KeyVo key, KeyOwnerVo owner) {
        // write key record
        KeyModel keyModel = keyConverter.voToModel(key);

        // populate reverse lookup
        KeyByOwnerAndServiceVo keyByOwnerAndServiceVo = new KeyByOwnerAndServiceVo(key, owner);
        var keyByOwnerAndServiceModel = keyByOwnerAndServiceConverter.voToModel(
                keyByOwnerAndServiceVo);

        long timestamp = writeTimestamp();
        CassandraBatchOperations batchOperations = cassandraTemplate.batchOps()
                .withTimestamp(timestamp)
                .insert(List.of(keyModel))
                .insert(List.of(keyByOwnerAndServiceModel));

        try {
            if (!batchOperations.execute().wasApplied()) {
                throw new CassandraException("Failed to write key into db");
            }
        } catch (TransientDataAccessException | DataAccessResourceFailureException e) {
            log.warn("logged batch write of key failed, writing rows directly", e);
            InsertOptions options = InsertOptions.builder()
                    .timestamp(timestamp)
                    .idempotent(true)
                    .build();
            // list the key before making it valid, so a partial write never leaves a hidden key
            writeDirectly(() -> {
                cassandraTemplate.insert(keyByOwnerAndServiceModel, options);
                cassandraTemplate.insert(keyModel, options);
            });
        }
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

        var keyByOwnerAndServiceModel = keyByOwnerAndServiceConverter.voToModel(key);

        long timestamp = writeTimestamp();
        CassandraBatchOperations batchOperations = cassandraTemplate.batchOps()
                .withTimestamp(timestamp)
                .delete(List.of(keyModel))
                .delete(List.of(keyByOwnerAndServiceModel));

        try {
            if (!batchOperations.execute().wasApplied()) {
                throw new CassandraException("Failed to delete key.");
            }
        } catch (TransientDataAccessException | DataAccessResourceFailureException e) {
            log.warn("logged batch delete of key failed, deleting rows directly", e);
            DeleteOptions options = DeleteOptions.builder()
                    .timestamp(timestamp)
                    .idempotent(true)
                    .build();
            // revoke the key before removing it from the owner's list
            writeDirectly(() -> {
                cassandraTemplate.delete(keyModel, options);
                cassandraTemplate.delete(keyByOwnerAndServiceModel, options);
            });
        }
    }

    /**
     * Writes the rows of a logged batch one by one after the batch failed. The batch outcome is
     * unknown: a timed-out batch may still be replayed from the batchlog. The rows use the batch
     * timestamp, so writing them again leaves the same state as the batch. A failure here does not
     * advertise Retry-After: a client that retries a create generates a different key, while the
     * first one may already be stored.
     */
    private void writeDirectly(Runnable write) {
        try {
            write.run();
        } catch (TransientDataAccessException | DataAccessResourceFailureException e) {
            throw ServiceUnavailableException.withoutRetryAfter(
                    "Key storage is temporarily unavailable, the outcome of the write is unknown", e);
        }
    }

    private long writeTimestamp() {
        return session.getContext().getTimestampGenerator().next();
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


    private KeyVo modelToValidatedVo(KeyModel keyModel) {
        return keyConverter.modelToVo(keyModel);
    }

    private KeyByOwnerAndServiceVo modelToValidatedVo(KeyByOwnerAndServiceModel model) {
        return keyByOwnerAndServiceConverter.modelToVo(model);
    }
}
