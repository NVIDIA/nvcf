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


import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import java.time.Instant;
import org.springframework.data.cassandra.core.mapping.MapId;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Repository
public interface EncryptionKeyV2Repository
        extends ReactiveCassandraRepository<EncryptionKeyV2Model, MapId> {

    // decryption and point lookups (no status filter)
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<EncryptionKeyV2Model> findFirstByNamespaceAndKid(String namespace, String kid);

    @VisibleForTesting
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<EncryptionKeyV2Model> findAllByNamespaceAndKid(String namespace, String kid);

    @VisibleForTesting
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<EncryptionKeyV2Model> findAllByNamespace(String namespace);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<Slice<EncryptionKeyV2Model>> findAllByNamespace(String namespace, CassandraPageRequest pageRequest);

    // fail safe range query (no status filter)
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<EncryptionKeyV2Model> findFirstByNamespaceAndKidAndEncryptedAtLessThan(String namespace, String kid,
            Instant encryptedAt);

    @VisibleForTesting
    @Consistency(DefaultConsistencyLevel.EACH_QUORUM)
    Mono<Void> deleteByNamespaceAndKidAndEncryptedAt(String namespace, String kid, Instant encryptedAt);
}

