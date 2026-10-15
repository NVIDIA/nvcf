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

import java.security.GeneralSecurityException;
import javax.crypto.Mac;
import javax.crypto.SecretKey;
import javax.crypto.spec.SecretKeySpec;
import org.apache.commons.pool2.impl.GenericKeyedObjectPool;

public class HmacEncoder {

    public static final String HMAC_SHA_3_512 = "HMac/SHA3-512";
    public static final String HMAC_SHA_3_256 = "HMac/SHA3-256";

    private final GenericKeyedObjectPool<SecretKey, Mac> macWorkersPool;

    public HmacEncoder(GenericKeyedObjectPool<SecretKey, Mac> macWorkersPool) {
        this.macWorkersPool = macWorkersPool;
    }

    public byte[] hmac(String algorithm, byte[] key, byte[] message)
            throws GeneralSecurityException {
        SecretKey secretKey = new SecretKeySpec(key, algorithm);
        Mac mac = borrow(secretKey);
        try {
            return mac.doFinal(message);
        } finally {
            macWorkersPool.returnObject(secretKey, mac);
        }
    }

    private Mac borrow(SecretKey secretKey) throws GeneralSecurityException {
        try {
            return macWorkersPool.borrowObject(secretKey);
        } catch (GeneralSecurityException | RuntimeException e) {
            throw e;
        } catch (Exception e) {
            throw new IllegalStateException("unexpected exception borrowing Mac", e);
        }
    }
}
