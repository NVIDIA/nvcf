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
package com.nvidia.nvcf.persistence.function;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import org.springframework.data.cassandra.repository.CassandraRepository;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;

@Repository
public interface FunctionsRepository extends
        CassandraRepository<FunctionEntity, UUID> {

    Optional<FunctionEntity> getByFunctionVersionId(UUID functionVersionId);

    Slice<FunctionEntity> findAllBy(Pageable pageable);

    // TODO: remove. Only used for background tasks
    Stream<FunctionEntity> findAllBy();

    Stream<FunctionEntity> findAllByNcaId(String ncaId);

    Stream<FunctionEntity> findAllByFunctionId(UUID functionId);

    Stream<FunctionEntity> findAllByNcaIdAndFunctionId(String ncaId, UUID functionId);

    Optional<FunctionEntity> getByNcaIdAndFunctionIdAndFunctionVersionId(
            String ncaId, UUID functionId, UUID functionVersionId);

    // needed for perf because count is used in function creation which in some cases is done in large batches
    @Consistency(DefaultConsistencyLevel.LOCAL_ONE)
    long countByNcaId(String ncaId);

    @Query("SELECT * FROM " + FunctionEntity.TABLE_NAME + " WHERE " +
            FunctionEntity.COLUMN_FUNCTION_LEVEL_AUTHZ_ACCOUNTS + " CONTAINS :ncaId")
    Stream<FunctionEntity> findAllByFunctionLevelAuthorizedAccount(@Param("ncaId") String ncaId);

    @Query("SELECT * FROM " + FunctionEntity.TABLE_NAME + " WHERE " +
            FunctionEntity.COLUMN_VERSION_LEVEL_AUTHZ_ACCOUNTS + " CONTAINS :ncaId")
    Stream<FunctionEntity> findAllByVersionLevelAuthorizedAccount(@Param("ncaId") String ncaId);
}
