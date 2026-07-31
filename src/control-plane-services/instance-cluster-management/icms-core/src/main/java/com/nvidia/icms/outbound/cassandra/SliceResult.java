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

import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;
import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;

import java.nio.ByteBuffer;
import lombok.AllArgsConstructor;
import lombok.Data;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Slice;

@AllArgsConstructor
@Data
public class SliceResult<T> {


    /**
     * Spring boot uses "Slice" type of result as a wrapper around Cassandra pages.
     * Slice has result set of records and extra data to crate a cursor, which has to used to
     * fetch a next page from database.
     */
    private Slice<T> result;

    /**
     * cursor for getting the NEXT page, not for the current one
     */
    private String nextCursor;

    /**
     * Number of records in the page.
     */
    private int limit;

    /**
     * Constructor for slice result
     *
     * @param newResult - slice result from Cassandra
     * @param limit     - number of record can be used for the next page request
     */

    public SliceResult(Slice<T> newResult, int limit) {
        result = newResult;

        this.limit = limit;

        nextCursor = generateNextCursor();
    }

    /**
     * Generate a page request for database based on nextCursor and limit
     *
     * @return an instance of request that has to  be passed to database framework
     */
    public CassandraPageRequest generateCassandraPageRequest() {
        return generateCassandraPageRequest(getNextCursor(), getLimit());
    }


    /**
     * Checks if another request for data can be done by using this instance
     *
     * @return True if the next page can be requested
     */
    public boolean canBeRequested() {
        // if getResult() is null it means that data was not requested yet.
        return getResult() == null || hasNextData();
    }

    /**
     * Checks if slice result has a flag indicating for next page availability
     *
     * @return True if another page is available
     */
    public boolean hasNextData() {
        return getResult() != null && getResult().hasNext();
    }

    /**
     * Generates a cursor for generating page request for the next page if available
     *
     * @return Cursor value if it can be generated, null otherwise.
     */
    public String generateNextCursor() {
        if (hasNextData()) {
            ByteBuffer pagingState = ((CassandraPageRequest) result.getPageable()).getPagingState();
            return toHexString(pagingState);
        } else {
            return null;
        }
    }

    public static CassandraPageRequest generateCassandraPageRequest(String cursor, int limit) {
        ByteBuffer byteBuffer = cursor == null ? null : fromHexString(cursor);
        PageRequest pageable = PageRequest.of(0, limit);
        return CassandraPageRequest.of(pageable, byteBuffer);
    }

}
