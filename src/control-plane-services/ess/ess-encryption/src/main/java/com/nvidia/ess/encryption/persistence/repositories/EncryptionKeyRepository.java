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
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import org.springframework.data.cassandra.core.mapping.MapId;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

// only v1 tables
@Repository
public interface EncryptionKeyRepository
        extends ReactiveCassandraRepository<EncryptionKeyModel, MapId> {
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<EncryptionKeyModel> findByNamespaceAndKid(String namespace, String kid);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<Slice<EncryptionKeyModel>> findAllByNamespace(String namespace, CassandraPageRequest pageRequest);

    @VisibleForTesting
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<EncryptionKeyModel> findAllByNamespace(String namespace);
}

