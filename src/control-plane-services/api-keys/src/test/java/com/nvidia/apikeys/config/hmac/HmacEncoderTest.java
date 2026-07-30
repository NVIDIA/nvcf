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

package com.nvidia.apikeys.config.hmac;

import static com.nvidia.apikeys.config.hmac.HmacEncoder.HMAC_SHA_3_256;
import static org.assertj.core.api.Assertions.assertThat;

import java.util.HexFormat;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import org.apache.commons.pool2.impl.GenericKeyedObjectPool;
import org.apache.commons.pool2.impl.GenericKeyedObjectPoolConfig;
import org.junit.jupiter.api.Test;

class HmacEncoderTest {

    private final HmacEncoder encoder = new HmacEncoder(new GenericKeyedObjectPool<>(
            new HmacWorkersPooledFactory(), new GenericKeyedObjectPoolConfig<>()));

    @Test
    void concurrentBorrowsProduceCorrectOutput() throws Exception {
        int threads = 16;
        int iterationsPerThread = 64;
        ExecutorService pool = Executors.newFixedThreadPool(threads);
        CountDownLatch start = new CountDownLatch(1);
        CountDownLatch done = new CountDownLatch(threads);

        byte[] key = "shared-key".getBytes();
        byte[] message = "shared-message".getBytes();
        String expected = referenceHmac(key, message);

        try {
            for (int t = 0; t < threads; t++) {
                pool.submit(() -> {
                    try {
                        start.await();
                        for (int i = 0; i < iterationsPerThread; i++) {
                            byte[] out = encoder.hmac(HMAC_SHA_3_256, key, message);
                            if (!HexFormat.of().formatHex(out).equals(expected)) {
                                throw new AssertionError("mismatch on iteration " + i);
                            }
                        }
                    } catch (Exception e) {
                        throw new RuntimeException(e);
                    } finally {
                        done.countDown();
                    }
                });
            }
            start.countDown();
            assertThat(done.await(30, TimeUnit.SECONDS)).isTrue();
        } finally {
            pool.shutdownNow();
        }
    }

    private static String referenceHmac(byte[] key, byte[] message) throws Exception {
        Mac mac = Mac.getInstance(HMAC_SHA_3_256, "BC");
        mac.init(new SecretKeySpec(key, HMAC_SHA_3_256));
        return HexFormat.of().formatHex(mac.doFinal(message));
    }
}
