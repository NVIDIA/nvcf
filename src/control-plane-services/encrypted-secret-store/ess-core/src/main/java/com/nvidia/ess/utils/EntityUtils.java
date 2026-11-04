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
package com.nvidia.ess.utils;

import com.google.common.hash.Hashing;
import java.nio.charset.StandardCharsets;
import lombok.experimental.UtilityClass;

@UtilityClass
public class EntityUtils {

    public String getEntity(String entityType, String entityId) {
        return entityType + "/" + entityId;
    }

    /**
     * Return hash bucket for entity id
     *
     * Possible return values are
     *
     * [0 * SIGNED_INT_MAX / hash_size,
     *  1 * SIGNED_INT_MAX / hash_size,
     *  2 * SIGNED_INT_MAX / hash_size,
     *  …,
     * (hash_size) - 1 * SIGNED_INT_MAX / hash_size]
     *
     * Any hash between the bucket indices will be assigned to the lower bucket. Example: hash = (2 * SIGNED_INT_MAX / hash_size) + 1 belongs between
     * [2 * SIGNED_INT_MAX / hash_size, 3 * SIGNED_INT_MAX / hash_size]. This means that the assigned bucket will be the start of the range, 2 * SIGNED_INT_MAX / hash_size.
     *
     * @param entityId entity id
     * @param hashSize number of equally spread buckets
     * @return hash bucket
     */
    public int getEntityIdBucket(String entityId, int hashSize) {
        int hash = Hashing.murmur3_32_fixed().hashString(entityId, StandardCharsets.UTF_8).asInt();

        int positiveHash;
        if (hash == Integer.MIN_VALUE) {
            // Integer.MIN_VALUE overflows on Math.abs(). Assign to bucket 0
            positiveHash = 0;
        } else {
            positiveHash = Math.abs(hash);
        }

        // round up / ceiling
        int bucketSize = Integer.MAX_VALUE / hashSize + (Integer.MAX_VALUE % hashSize == 0 ? 0 : 1);

        // hash is guaranteed to be less or equals to Integer.MAX_VALUE, so division result
        // will always be less than hashSize because bucketSize is rounded up
        int bucketIndex = positiveHash / bucketSize;

        return bucketIndex * bucketSize;
    }
}
