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
package com.nvidia.icms.outbound.cassandra;

import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.function.Consumer;
import java.util.function.Function;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;

public interface IcmsDatabaseRepository<T> {

    <S extends T> boolean insertWithTtl(S entity, Duration ttl, boolean insertOnlyIfNotExists);

    <S extends T> boolean isInserted(S entity);

    <S extends T> boolean isInsertedIfNotExists(S entity);

    <S extends T> T update(S entity);

    <S extends T> S updateWithInsertNulls(S entity);

    void applyActions(
                    @NotNull Function<Pageable, Slice<T>> dataProvider,
                    @NotNull Consumer<T> action,
                    int pageSize,
                    int pauseBetweenPagesInMs,
                    int pauseBetweenRecordsInMs);
}
