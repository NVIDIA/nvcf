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

package com.nvidia.icms.outbound.cassandra.workers;

import com.nvidia.icms.outbound.cassandra.IcmsDatabaseRepository;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierKey;
import com.nvidia.icms.outbound.cassandra.workers.entity.WorkerIdentifierRecord;
import java.util.List;
import java.util.Optional;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.stereotype.Repository;

@Repository
public interface WorkerIdentifierRepo extends
        CassandraRepository<WorkerIdentifierRecord, WorkerIdentifierKey>,
        IcmsDatabaseRepository<WorkerIdentifierRecord> {

    Optional<WorkerIdentifierRecord> findByKeyClusterIdAndKeyInstanceId(
            String clusterId, String instanceId);

    /** Partition-restricted lookup through the SAI index on {@code sa_uid}. */
    List<WorkerIdentifierRecord> findByKeyClusterIdAndSaUid(String clusterId, String saUid);

    void deleteByKeyClusterIdAndKeyInstanceId(String clusterId, String instanceId);

    /** Drops the whole cluster partition (cluster deregistration). */
    void deleteByKeyClusterId(String clusterId);
}
