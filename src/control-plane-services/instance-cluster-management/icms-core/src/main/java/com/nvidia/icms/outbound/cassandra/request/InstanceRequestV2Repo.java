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
package com.nvidia.icms.outbound.cassandra.request;

import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.nvidia.icms.outbound.cassandra.IcmsDatabaseRepository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import java.util.UUID;
import java.util.stream.Stream;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.stereotype.Repository;

@Repository
public interface InstanceRequestV2Repo extends
        CassandraRepository<InstanceRequestV2Entity, String>,
        IcmsDatabaseRepository<InstanceRequestV2Entity> {

    Stream<InstanceRequestV2Entity> findAllByDeploymentId(UUID deploymentId);

    Stream<InstanceRequestV2Entity> findAllByNcaId(String ncaId);

    /**
     * Backed by the {@code idx_requests_by_creation_day} storage-attached index.
     * Used only when {@code icms.creation-bucket-reads-enabled} is on.
     */
    @Query("SELECT request_id FROM requests WHERE creation_bucket = :creationBucket")
    ResultSet findRequestIdsByCreationBucket(@Param("creationBucket") Instant creationBucket);

    @Query("UPDATE requests USING TIMESTAMP :writeTimestamp "
            + "SET creation_bucket = :creationBucket WHERE request_id = :requestId")
    void updateCreationBucket(
            @Param("requestId") String requestId,
            @Param("creationBucket") Instant creationBucket,
            @Param("writeTimestamp") long writeTimestamp);
}
