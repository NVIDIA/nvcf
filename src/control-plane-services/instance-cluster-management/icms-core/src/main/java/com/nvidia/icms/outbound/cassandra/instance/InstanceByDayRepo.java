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
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayKey;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.data.domain.Limit;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;

@Repository
public interface InstanceByDayRepo extends
        CassandraRepository<InstanceByDayEntity, InstanceByDayKey>,
        IcmsDatabaseRepository<InstanceByDayEntity> {

    List<InstanceByDayEntity> findAllByKeyTruncatedTsByDay(Instant day);

    List<InstanceByDayEntity> findAllByKeyTruncatedTsByDay(Instant day, Limit limit);

    Slice<InstanceByDayEntity> findAllByKeyTruncatedTsByDay(Instant day, Pageable pageable);

    Optional<InstanceByDayEntity> findByKeyTruncatedTsByDayAndKeyInstanceId(Instant day, String instanceId);

    boolean deleteByKeyTruncatedTsByDay(Instant day);
}