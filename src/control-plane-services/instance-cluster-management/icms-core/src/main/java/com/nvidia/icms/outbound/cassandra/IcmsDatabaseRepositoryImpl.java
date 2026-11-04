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
package com.nvidia.icms.outbound.cassandra;

import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;

import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import jakarta.validation.constraints.NotNull;
import java.nio.ByteBuffer;
import java.util.Objects;
import java.util.function.Consumer;
import java.util.function.Function;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.data.cassandra.core.CassandraOperations;
import org.springframework.data.cassandra.core.EntityWriteResult;
import org.springframework.data.cassandra.core.InsertOptions;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;

@Slf4j
public class IcmsDatabaseRepositoryImpl<T> implements IcmsDatabaseRepository<T> {
    @Autowired
    private CassandraOperations operations;

    @Override
    public <S extends T> boolean insertWithTtl(S entity, Duration ttl, boolean insertOnlyIfNotExists) {
        InsertOptions insertOptions = InsertOptions.builder()
                .ttl(ttl)
                .ifNotExists(insertOnlyIfNotExists)
                .withInsertNulls(false)
                .build();
        EntityWriteResult<S> r = operations.insert(entity, insertOptions);
        return r.wasApplied();
    }

    @Override
    public <S extends T> boolean isInserted(S entity) {
        InsertOptions insertOptions = InsertOptions.builder()
                .ifNotExists(false)
                .withInsertNulls(false)
                .build();
        EntityWriteResult<S> r = operations.insert(entity, insertOptions);
        return r.wasApplied();
    }

    @Override
    public <S extends T> boolean isInsertedIfNotExists(S entity) {
        InsertOptions insertOptions = InsertOptions.builder()
                .ifNotExists(true)
                .withInsertNulls(false)
                .build();
        EntityWriteResult<S> r = operations.insert(entity, insertOptions);
        return r.wasApplied();
    }

    public <S extends T> S update(S entity) {
        InsertOptions insertOptions = InsertOptions.builder()
                .ifNotExists(false)
                .withInsertNulls(false)
                .build();
        EntityWriteResult<S> r = operations.insert(entity, insertOptions);
        return r.getEntity();
    }

    public <S extends T> S updateWithInsertNulls(S entity) {
        InsertOptions insertOptions = InsertOptions.builder()
                .ifNotExists(false)
                .withInsertNulls(true)
                .build();
        EntityWriteResult<S> r = operations.insert(entity, insertOptions);
        return r.getEntity();
    }

    public void applyActions(
            @NotNull Function<Pageable,
            Slice<T>> dataProvider,
            @NotNull Consumer<T> action,
            int pageSize,
            int pauseBetweenPagesInMs,
            int pauseBetweenRecordsInMs) {
        Objects.requireNonNull(dataProvider);
        Objects.requireNonNull(action);

        SliceResult<T> entitySlice = new SliceResult<>(null, pageSize);
        do {
            if (entitySlice.canBeRequested()) {
                entitySlice = new SliceResult<>(
                        dataProvider.apply(entitySlice.generateCassandraPageRequest()),
                        entitySlice.getLimit());

                if (entitySlice.getResult() != null) {
                    for (T enity : entitySlice.getResult()) {
                        action.accept(enity);
                        sleep(pauseBetweenRecordsInMs);
                    }
                }
            } else {
                break;
            }

           sleep(pauseBetweenPagesInMs);
        } while (entitySlice.hasNextData());
    }

    private void sleep(int timeMs) {
        if (timeMs > 0) {
            try {
                Thread.sleep(timeMs); // sleep to avoid DDoS on DB
            } catch (InterruptedException e) {
                log.error("Error of pausing the thread error: {}", e.getMessage(), e);
                Thread.currentThread().interrupt();
            }
        }
    }
}
