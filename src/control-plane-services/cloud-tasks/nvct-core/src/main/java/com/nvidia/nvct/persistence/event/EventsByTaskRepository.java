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
package com.nvidia.nvct.persistence.event;

import com.nvidia.nvct.persistence.event.entity.EventByTaskEntity;
import com.nvidia.nvct.persistence.event.entity.EventByTaskKey;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;

@Repository
public interface EventsByTaskRepository
        extends CassandraRepository<EventByTaskEntity, EventByTaskKey> {

    Optional<EventByTaskEntity> getByKeyTaskIdAndKeyEventId(UUID taskId, UUID eventId);

    Stream<EventByTaskEntity> findByKeyTaskId(UUID taskId);

    Slice<EventByTaskEntity> findByKeyTaskId(UUID taskId, Pageable pageable);

    void deleteByKeyTaskId(UUID taskId);

}
