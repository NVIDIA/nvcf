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
package com.nvidia.nvct.persistence.result;

import com.nvidia.nvct.persistence.result.entity.ResultByTaskEntity;
import com.nvidia.nvct.persistence.result.entity.ResultByTaskKey;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Repository;

@Repository
public interface ResultsByTaskRepository
        extends CassandraRepository<ResultByTaskEntity, ResultByTaskKey> {

    Optional<ResultByTaskEntity> getByKeyTaskIdAndKeyResultId(UUID taskId, UUID resultId);

    Stream<ResultByTaskEntity> findByKeyTaskId(UUID taskId);

    Slice<ResultByTaskEntity> findByKeyTaskId(UUID taskId, Pageable pageable);

    void deleteByKeyTaskId(UUID taskId);

}
