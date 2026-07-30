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

package com.nvidia.apikeys.services;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import org.bouncycastle.util.encoders.Hex;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

/**
 * This service just wraps the hashing functionality so it can be wired as a bean where it is needed.
 */
@Service
@RefreshScope
public class HashingService {

    private final MessageDigest sha256digest;

    public HashingService()
            throws NoSuchAlgorithmException {
        sha256digest = MessageDigest.getInstance("SHA-256");
    }

    /**
     * Use this method to generate hash256 of a string according to <a href="https://csrc.nist.gov/csrc/media/publications/fips/180/2/archive/2002-08-01/documents/fips180-2withchangenotice.pdf">Federal Information Processing Standards Publication 180-2</a>
     * Result is verifiable with unix tools like `sha256sum`.
     * Example call would look like `echo -n "string" | sha256sum`
     */
    public String sha256(String data) {
        return new String(Hex.encode(sha256digest.digest(data.getBytes(StandardCharsets.UTF_8))));
    }

}
