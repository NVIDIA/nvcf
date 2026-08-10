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
package com.nvidia.ess.persistence.repositories;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.ess.persistence.models.SecretPathModel;
import java.util.Collection;
import org.springframework.data.cassandra.core.mapping.MapId;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Repository
public interface SecretPathRepository extends ReactiveCassandraRepository<SecretPathModel, MapId>, SecretPathRepositoryCustomWriteOps {

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<SecretPathModel> findAllByNamespaceAndEntityAndPathIn(String namespace, String entity, Collection<String> paths);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<SecretPathModel> findAllByNamespaceAndEntity(String namespace, String entity);

    // keeping for future if optimization is need on fetch
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<SecretPathModel> findAllByNamespaceAndEntityAndPathAfterAndPathBefore(String namespace, String entity, String pathStart, String pathEnd);

    Mono<Void> deleteByNamespaceAndEntity(String namespace, String entity);
}
