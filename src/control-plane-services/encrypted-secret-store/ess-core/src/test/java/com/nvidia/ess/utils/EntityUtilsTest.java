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

import java.util.Random;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import org.apache.commons.lang3.RandomStringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;

class EntityUtilsTest {


    @Test
    void getEntityIdBucket_onRandomizedParams_fallsIntoPossibleHashSizeBuckets() {
        Random rand = new Random();
        for (int i = 0; i < 100; i++) {
            int hashSize = rand.nextInt(1, 1000);
            Set<Integer> possibleBuckets = getPossibleBuckets(hashSize);

            for (int j = 0; j < 100; j++) {
                String entityId = RandomStringUtils.randomAscii(2, 100);
                Assertions.assertTrue(possibleBuckets.contains(EntityUtils.getEntityIdBucket(entityId, hashSize)));
            }
        }
    }

    private Set<Integer> getPossibleBuckets(int hashSize) {
        int bucketSize = Integer.MAX_VALUE / hashSize + (Integer.MAX_VALUE % hashSize == 0 ? 0 : 1);

        return IntStream.range(0, hashSize)
                .boxed()
                .map(i -> i * bucketSize)
                .collect(Collectors.toSet());
    }
}
