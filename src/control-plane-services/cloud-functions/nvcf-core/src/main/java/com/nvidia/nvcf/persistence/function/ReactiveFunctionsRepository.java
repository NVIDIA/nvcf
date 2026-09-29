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

import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

public interface ReactiveFunctionsRepository extends
        ReactiveCassandraRepository<FunctionEntity, UUID> {

    /**
     * Not intended for external use. This is the only way to build native CompletableFuture.
     * Spring Boot Data generated CompletableFuture always have Completed Status and is
     * executed consecutive.
     */
    Mono<FunctionEntity> getByFunctionVersionId(UUID functionVersionId);

    default CompletableFuture<Optional<FunctionEntity>> asyncGetByFunctionIdAndFunctionVersionId(
            UUID functionId,
            UUID functionVersionId) {
        return getByFunctionVersionId(functionVersionId).toFuture().thenApply(Optional::ofNullable);
    }

    /**
     * Not intended for external use. This is the only way to build native CompletableFuture.
     * Spring Boot Data generated CompletableFuture always have Completed Status and is
     * executed consecutive.
     */
    Flux<FunctionEntity> findByFunctionId(UUID functionId);

    default CompletableFuture<List<FunctionEntity>> asyncFindByKeyFunctionId(UUID functionId) {
        return findByFunctionId(functionId).collectList().toFuture();
    }
}
