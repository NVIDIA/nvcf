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

import com.nvidia.icms.outbound.cassandra.IcmsDatabaseRepository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayKey;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.data.domain.Limit;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;

@Repository
public interface InstanceRequestV2ByDayRepo extends
        CassandraRepository<InstanceRequestV2ByDayEntity, InstanceRequestV2ByDayKey>,
        IcmsDatabaseRepository<InstanceRequestV2ByDayEntity> {

    Optional<InstanceRequestV2ByDayEntity> findByKeyTruncatedTsByDayAndKeyRequestId(
            Instant day, String requestId);

    Slice<InstanceRequestV2ByDayEntity> findAllByKeyTruncatedTsByDay(Instant day, Pageable pageable);

    List<InstanceRequestV2ByDayEntity> findAllByKeyTruncatedTsByDay(Instant day, Limit limit);

    boolean deleteByKeyTruncatedTsByDay(Instant day);

}
