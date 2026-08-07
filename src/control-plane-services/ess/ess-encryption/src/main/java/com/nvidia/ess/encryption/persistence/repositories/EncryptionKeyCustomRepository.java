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
package com.nvidia.ess.encryption.persistence.repositories;


import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.querybuilder.QueryBuilder;
import com.datastax.oss.driver.api.querybuilder.relation.Relation;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.AllValidStatus;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2PartitionModel;
import io.opentelemetry.instrumentation.annotations.WithSpan;
import java.time.Instant;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.data.cassandra.core.WriteResult;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

// only v1 tables
@Slf4j
@Repository
public class EncryptionKeyCustomRepository {

    @Setter(onMethod_ = {@Autowired})
    private ReactiveCassandraTemplate reactiveCassandraTemplate;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    // Batch operations in cassandra are not traced properly.
    // The span operation name will show up as the db.name or keyspace
    // The statements themselves will not show up either.
    @WithSpan
    public Mono<Boolean> addKey(EncryptionKeyV2Model encryptionKeyModel) {
        return reactiveCassandraTemplate.batchOps()
                .insert(encryptionKeyModel.toEncryptionKeyByKidModel())
                .insert(encryptionKeyModel.toEncryptionKeyByTimestampModel())
                .execute()
                .map(WriteResult::wasApplied);
    }

    @WithSpan
    public Mono<Boolean> addKeyV2(EncryptionKeyV2Model encryptionKeyV2Model) {
        // flipping to avoid CAS timeouts on multiple writes to the same partition
        return addKeyV2(encryptionKeyV2Model, CurrentKidWriteAction.PERSIST_IF_VALIDATED, false);
    }

    public enum CurrentKidWriteAction {
        DONT_PERSIST,
        PERSIST_IF_VALIDATED,
        FORCE_PERSIST
    }

    @WithSpan
    public Mono<Boolean> addKeyV2(EncryptionKeyV2Model encryptionKeyV2Model,
            CurrentKidWriteAction currentKidWriteAction, boolean insertOnlyIfNotExists) {

        var insert = QueryBuilder.insertInto(encryptionProperties.getTableNameByKidAndEncryptedAt())
                .value(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE,
                        QueryBuilder.literal(encryptionKeyV2Model.getNamespace()))
                .value(EncryptionKeyV2PartitionModel.COLUMN_KID,
                        QueryBuilder.literal(encryptionKeyV2Model.getKid()))
                .value(EncryptionKeyV2Model.COLUMN_CREATED_AT,
                        QueryBuilder.literal(encryptionKeyV2Model.getCreatedAt()))
                .value(EncryptionKeyV2PartitionModel.COLUMN_ENCRYPTED_AT,
                        QueryBuilder.literal(encryptionKeyV2Model.getEncryptedAt()))
                .value(EncryptionKeyV2Model.COLUMN_ENCRYPTED_BY_KID,
                        QueryBuilder.literal(encryptionKeyV2Model.getEncryptedByKid()))
                .value(EncryptionKeyV2Model.COLUMN_ENCRYPTED_KEY,
                        QueryBuilder.literal(encryptionKeyV2Model.getEncryptedKey()))
                .value(EncryptionKeyV2Model.COLUMN_STATUS,
                        QueryBuilder.literal(encryptionKeyV2Model.getStatus()));

        if (currentKidWriteAction == CurrentKidWriteAction.FORCE_PERSIST) {
            // Caller asks to force-persist the `current_kid` in the model irrespective of its
            // `status` or whether the value of its `kid` converges with its `current_kid`.
            insert = insert.value(EncryptionKeyV2PartitionModel.COLUMN_CURRENT_KID,
                        QueryBuilder.literal(encryptionKeyV2Model.getCurrentKid()));

        } else if (currentKidWriteAction == CurrentKidWriteAction.PERSIST_IF_VALIDATED &&
               AllValidStatus.allValidStatusStrings().contains(encryptionKeyV2Model.getStatus())) {
            
            if (encryptionKeyV2Model.getKid().equals(encryptionKeyV2Model.getCurrentKid())) {
                // Inserting a validated NEK. The `current_kid` needs to be updated to this
                // NEK's `kid`.
                insert = insert.value(EncryptionKeyV2PartitionModel.COLUMN_CURRENT_KID,
                        QueryBuilder.literal(encryptionKeyV2Model.getCurrentKid()));
            } else {
                log.error("Ignoring attempt to persist column: `current_kid` in EncryptionKeyV2Model " +
                        "with `namespace` = {}, `kid` = {}, `current_kid` = {}, `status` = {} (validated)",
                        encryptionKeyV2Model.getNamespace(), maskKid(encryptionKeyV2Model.getKid()),
                        maskKid(encryptionKeyV2Model.getCurrentKid()), encryptionKeyV2Model.getStatus());
            }
        }

        return reactiveCassandraTemplate.getReactiveCqlOperations()
                .execute(insertOnlyIfNotExists ? insert.ifNotExists().build() : insert.build());
    }

    @WithSpan
    public Mono<Boolean> updateStatus(String namespace, String kid, Instant encryptedAt, String newStatus) {
        var update = QueryBuilder.update(encryptionProperties.getTableNameByKidAndEncryptedAt())
                .setColumn(EncryptionKeyV2Model.COLUMN_STATUS, QueryBuilder.literal(newStatus))
                .where(Relation.column(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE)
                                .isEqualTo(QueryBuilder.literal(namespace)),
                        Relation.column(EncryptionKeyV2PartitionModel.COLUMN_KID)
                                .isEqualTo(QueryBuilder.literal(kid)),
                        Relation.column(EncryptionKeyV2PartitionModel.COLUMN_ENCRYPTED_AT)
                                .isEqualTo(QueryBuilder.literal(encryptedAt)));

        return reactiveCassandraTemplate.getReactiveCqlOperations()
                .execute(update.build());
    }

    @WithSpan
    public Mono<Boolean> updateStatusAndCurrentKid(String namespace, String kid, Instant encryptedAt,
            String newStatus, String newCurrentKid) {
        var update = QueryBuilder.update(encryptionProperties.getTableNameByKidAndEncryptedAt())
                .setColumn(EncryptionKeyV2Model.COLUMN_STATUS, QueryBuilder.literal(newStatus))
                .setColumn(EncryptionKeyV2PartitionModel.COLUMN_CURRENT_KID, QueryBuilder.literal(newCurrentKid))
                .where(Relation.column(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE)
                                .isEqualTo(QueryBuilder.literal(namespace)),
                        Relation.column(EncryptionKeyV2PartitionModel.COLUMN_KID)
                                .isEqualTo(QueryBuilder.literal(kid)),
                        Relation.column(EncryptionKeyV2PartitionModel.COLUMN_ENCRYPTED_AT)
                                .isEqualTo(QueryBuilder.literal(encryptedAt)));

        return reactiveCassandraTemplate.getReactiveCqlOperations()
                .execute(update.build());
    }

    @WithSpan
    public Mono<Boolean> updateCurrentKidIfNotSet(String namespace, String newCurrentKid) {
        var update = QueryBuilder.update(encryptionProperties.getTableNameByKidAndEncryptedAt())
                .setColumn(EncryptionKeyV2PartitionModel.COLUMN_CURRENT_KID, QueryBuilder.literal(newCurrentKid))
                .where(Relation.column(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE)
                                .isEqualTo(QueryBuilder.literal(namespace)))
                .ifColumn(EncryptionKeyV2PartitionModel.COLUMN_CURRENT_KID)
                .isEqualTo(QueryBuilder.literal(null));

        return reactiveCassandraTemplate.getReactiveCqlOperations().execute(update.build());
    }

    @WithSpan
    public Mono<Slice<String>> findAllKidsForNamespaceInNEKv2NonDistinct(String namespace,
            CassandraPageRequest pageable) {

        var select = QueryBuilder.selectFrom(encryptionProperties.getTableNameByKidAndEncryptedAt())
                .column(EncryptionKeyV2PartitionModel.COLUMN_KID)
                .whereColumn(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE)
                                    .isEqualTo(QueryBuilder.literal(namespace))
                .build()
                .setPagingState(pageable.getPagingState())
                .setPageSize(pageable.getPageSize())
                .setConsistencyLevel(ConsistencyLevel.LOCAL_QUORUM);

        // cassandra template will use prepared statements internally
        return reactiveCassandraTemplate.slice(select, String.class);
    }

    @WithSpan
    public Mono<Slice<String>> findAllDistinctNamespacesInNEKv2(CassandraPageRequest pageable) {
        var select =
                QueryBuilder.selectFrom(encryptionProperties.getTableNameByKidAndEncryptedAt()).distinct()
                        .column(EncryptionKeyV2PartitionModel.COLUMN_NAMESPACE)
                        .build()
                        .setPagingState(pageable.getPagingState())
                        .setPageSize(pageable.getPageSize())
                        .setConsistencyLevel(ConsistencyLevel.LOCAL_QUORUM);

        // cassandra template will use prepared statements internally
        return reactiveCassandraTemplate.slice(select, String.class);
    }

    @WithSpan
    public Mono<Slice<String>> findAllDistinctNamespacesInNEKv1(CassandraPageRequest pageable) {
        var select =
                QueryBuilder.selectFrom(encryptionProperties.getTableNameByTimestamp()).distinct()
                        .column(EncryptionKeyModel.COLUMN_NAMESPACE)
                        .build()
                        .setPagingState(pageable.getPagingState())
                        .setPageSize(pageable.getPageSize())
                        .setConsistencyLevel(ConsistencyLevel.LOCAL_QUORUM);

        // cassandra template will use prepared statements internally
        return reactiveCassandraTemplate.slice(select, String.class);
    }

    @WithSpan
    public Mono<Slice<EncryptionKeyV2Model>> findAllV2Keys(CassandraPageRequest pageable) {
        var select =
                QueryBuilder.selectFrom(encryptionProperties.getTableNameByKidAndEncryptedAt())
                        .all()
                        .build()
                        .setPagingState(pageable.getPagingState())
                        .setPageSize(pageable.getPageSize())
                        .setConsistencyLevel(ConsistencyLevel.LOCAL_QUORUM);

        // cassandra template will use prepared statements internally
        return reactiveCassandraTemplate.slice(select, EncryptionKeyV2Model.class);
    }
}

