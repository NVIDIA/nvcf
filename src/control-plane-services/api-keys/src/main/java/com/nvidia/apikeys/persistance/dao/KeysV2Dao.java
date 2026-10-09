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

import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyV2Model;
import com.nvidia.apikeys.persistance.models.KeysByOwnerAndAccountAndServiceModel;
import com.nvidia.apikeys.persistance.repositories.KeyV2Repository;
import com.nvidia.apikeys.persistance.repositories.KeysByOwnerAndAccountAndServiceRepository;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByAccountSliceVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import java.nio.ByteBuffer;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.function.Function;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

/**
 * Persistence for keys issued to an account. Every key has a hash lookup row in keys_v2, read by
 * introspection, and a management row in keys_by_owner_and_account_and_service. Saves and
 * deletes write both rows in one logged batch.
 *
 * <p>Legacy keys stay in keys and keys_by_owner_and_service and are handled by {@link KeysDao}.
 */
@Service
@RequiredArgsConstructor
public class KeysV2Dao {

    private static final String MESG_INVALID_CURSOR = "Invalid cursor: '%s'";

    private final KeyV2Repository keyRepository;
    private final KeysByOwnerAndAccountAndServiceRepository keysByOwnerAndAccountAndServiceRepository;
    private final KeyV2ModelConverter keyConverter;
    private final CassandraTemplate cassandraTemplate;

    public KeysByOwnerAndAccountAndServiceVo save(KeyV2Vo key) {
        Objects.requireNonNull(key.getNcaId(), "ncaId is required");
        var keyModel = keyConverter.voToModel(key);
        var accountKeyModel = keyConverter.voToModel(KeysByOwnerAndAccountAndServiceVo.from(key));

        var writeResult = cassandraTemplate.batchOps()
                .insert(List.of(keyModel))
                .insert(List.of(accountKeyModel))
                .execute();

        if (!writeResult.wasApplied()) {
            throw new CassandraException("Failed to write key into db");
        }

        if (getKeyByHash(key.getKeyHash()).isEmpty()) {
            throw new CassandraException("Failed to read saved key");
        }

        return get(key.getNcaId(), key.getOwnerType(), key.getOwnerId(),
                   key.getIssuerServiceId(), key.getKeyId())
                .orElseThrow(() -> new CassandraException("Failed to read saved key owner"));
    }

    public Optional<KeyV2Vo> getKeyByHash(String hash) {
        return keyRepository.findByKeyHash(hash)
                .map(keyConverter::modelToVo);
    }

    public Optional<KeysByOwnerAndAccountAndServiceVo> get(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
                        ncaId, ownerType, ownerId, issuerServiceId, keyId)
                .map(keyConverter::modelToVo);
    }

    public Stream<KeysByOwnerAndAccountAndServiceVo> list(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerId(ncaId, ownerType, ownerId)
                .map(keyConverter::modelToVo);
    }

    public Stream<KeysByOwnerAndAccountAndServiceVo> list(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        return keysByOwnerAndAccountAndServiceRepository
                .findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceId(
                        ncaId, ownerType, ownerId, issuerServiceId)
                .map(keyConverter::modelToVo);
    }

    public KeysByAccountSliceVo list(String ncaId, int limit, String cursor) {
        return slice(
                pageable -> keysByOwnerAndAccountAndServiceRepository.findByNcaId(ncaId, pageable),
                limit, cursor);
    }

    public KeysByAccountSliceVo list(
            String ncaId, String issuerServiceId, int limit, String cursor) {
        return slice(
                pageable -> keysByOwnerAndAccountAndServiceRepository.findByNcaIdAndIssuerServiceId(
                        ncaId, issuerServiceId, pageable),
                limit, cursor);
    }

    public void delete(KeysByOwnerAndAccountAndServiceVo key) {
        var keyModel = KeyV2Model.builder()
                .keyHash(key.getKeyHash())
                .ncaId(key.getNcaId())
                .keyStatus(key.getKeyStatus())
                .build();

        var accountKeyModel = KeysByOwnerAndAccountAndServiceModel.builder()
                .ncaId(key.getNcaId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(key.getIssuerServiceId())
                .keyId(key.getKeyId())
                .build();

        var batchOperations = cassandraTemplate.batchOps()
                .delete(List.of(keyModel))
                .delete(List.of(accountKeyModel));

        if (!batchOperations.execute().wasApplied()) {
            throw new CassandraException("Failed to delete key.");
        }
    }

    private KeysByAccountSliceVo slice(
            Function<Pageable, Slice<KeysByOwnerAndAccountAndServiceModel>> query,
            int limit, String cursor) {
        var pageable = PageRequest.of(0, limit);
        Slice<KeysByOwnerAndAccountAndServiceModel> pagedResult;
        try {
            var pagingState = StringUtils.isBlank(cursor)
                    ? Optional.<ByteBuffer>empty()
                    : Optional.of(fromHexString(cursor));
            pagedResult = query.apply(CassandraPageRequest.of(pageable, pagingState.orElse(null)));
        } catch (RuntimeException e) {
            if (StringUtils.isBlank(cursor)) {
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
}
