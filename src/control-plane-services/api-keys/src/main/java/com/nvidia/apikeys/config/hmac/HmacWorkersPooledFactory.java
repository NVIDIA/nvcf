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

import java.security.Security;
import javax.crypto.Mac;
import javax.crypto.SecretKey;
import org.apache.commons.pool2.KeyedPooledObjectFactory;
import org.apache.commons.pool2.PooledObject;
import org.apache.commons.pool2.impl.DefaultPooledObject;
import org.bouncycastle.jce.provider.BouncyCastleProvider;

public class HmacWorkersPooledFactory implements KeyedPooledObjectFactory<SecretKey, Mac> {

    private static final String SECURITY_PROVIDER = "BC";

    static {
        Security.addProvider(new BouncyCastleProvider());
    }

    @Override
    public PooledObject<Mac> makeObject(SecretKey key) throws Exception {
        Mac mac = Mac.getInstance(key.getAlgorithm(), SECURITY_PROVIDER);
        return new DefaultPooledObject<>(mac);
    }

    @Override
    public void destroyObject(SecretKey key, PooledObject<Mac> p) {
        // let GC get it
    }

    @Override
    public boolean validateObject(SecretKey key, PooledObject<Mac> p) {
        boolean goodToReuse = false;
        try {
            p.getObject().reset();
            goodToReuse = true;
        } catch (Exception e) {
            // return false to evict defective object
        }
        return goodToReuse;
    }

    @Override
    public void activateObject(SecretKey key, PooledObject<Mac> p) throws Exception {
        p.getObject().init(key);
    }

    @Override
    public void passivateObject(SecretKey key, PooledObject<Mac> p) {
        p.getObject().reset();
    }
}
