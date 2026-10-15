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
package com.nvidia.icms.outbound.cassandra.instance;

import com.nvidia.icms.outbound.cassandra.IcmsDatabaseRepository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import java.util.UUID;
import java.util.stream.Stream;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.stereotype.Repository;

@Repository
public interface InstanceV2Repo extends
        CassandraRepository<InstanceV2Entity, String>,
        IcmsDatabaseRepository<InstanceV2Entity> {

    Stream<InstanceV2Entity> findByReservationId(UUID reservationId);

    /* This index presents in DB, but before use it chat with BradV
    Stream<InstanceV2Entity> findAllByNcaId(String ncaId);*/

    Stream<InstanceV2Entity> findAllByRequestId(String requestId);

    Stream<InstanceV2Entity> findAllByDeploymentId(UUID deploymentId);

    Stream<InstanceV2Entity> findAllByDeploymentIdAndGpuSpecificationId(
            UUID deploymentId, UUID gpuSpecificationId);

    /* This index presents in DB, but before use it chat with BradV
    Stream<InstanceV2Entity> findAllByZone(String zone); */
}
