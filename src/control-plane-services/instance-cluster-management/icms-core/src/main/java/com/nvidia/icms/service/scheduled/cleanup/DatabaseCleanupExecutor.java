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
package com.nvidia.icms.service.scheduled.cleanup;

import jakarta.annotation.Nullable;
import java.time.Instant;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;

public interface DatabaseCleanupExecutor<T> {

    /**
     * Find the first record from DB for that day.
     *
     * @param day
     * @return a first single record (whatever DB driver returns) if exists, or NULL otherwiose
     */
    @Nullable
    T getFirstRecord(Instant day);

    /**
     * Returns slice (page) or records for provided day. Cursor data and limit are provided inside of "pageable" structure
     *
     * @param day
     * @param pageable
     * @return Slice structure with result set of records and extra info for pagination
     */
    Slice<T> findRecordsPerDay(Instant day, Pageable pageable);

    /**
     * Validates if record was not marked as deleted and still can be used by checking isMarkedAsDeleted flag (usually)
     *
     * @param dbRecord
     * @return True if record was not marked as deleted
     */
    boolean isRecordActive(T dbRecord);

    /**
     * Delete all records from mapper table for desired day by deleting partition key. Usually used when all records for that day are marked as deleted.
     *
     * @param day
     */
    void deleteRecordsByDay(Instant day);

    /**
     * Executes clean up logic (like deleting records from other tables and etc.) for all active
     * records (not marked as deleted) for that day and then delete records from the table
     *
     * @param day
     */
    void cleanupRecordsByDay(Instant day);

}
