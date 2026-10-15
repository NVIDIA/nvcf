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

package com.nvidia.apikeys.validators;

import com.nvidia.apikeys.config.NakProperties;
import java.time.Duration;
import java.util.Base64;
import org.apache.commons.lang3.Strings;
import org.springframework.stereotype.Service;

@Service
public class NakPropertiesValidator {

    public static final int DATA_DOMAIN_KEY_LENGTH_BYTES = 136;
    public static final Duration MAX_KEEP_AFTER_EXPIRED_DURATION = Duration.ofDays(30);

    public static final String DATA_DOMAIN_KEY_INVALID_ERROR = String.format(
            "Data domain key length must be %d bytes", DATA_DOMAIN_KEY_LENGTH_BYTES);
    public static final String KEEP_AFTER_EXPIRED_DURATION_INVALID_ERROR = String.format(
            "keep-after-expired-duration must be set and not exceed %s", MAX_KEEP_AFTER_EXPIRED_DURATION);

    public NakProperties validate(NakProperties config) {
        validatePrefix(config.getKeyPrefix());
        validateDataDomainKey(config.getDataDomainKey());
        validateKeepAfterExpiredDuration(config.getKeepAfterExpiredDuration());
        return config;
    }

    private void validateKeepAfterExpiredDuration(Duration keepAfterExpiredDuration) {
        if (keepAfterExpiredDuration.isNegative()
                || keepAfterExpiredDuration.compareTo(MAX_KEEP_AFTER_EXPIRED_DURATION) > 0) {
            throw new IllegalStateException(KEEP_AFTER_EXPIRED_DURATION_INVALID_ERROR);
        }
    }

    private void validateDataDomainKey(String dataDomainKey) {
        byte[] keyBytes;
        try {
            keyBytes = Base64.getUrlDecoder().decode(dataDomainKey);
        } catch (RuntimeException decoderException) {
            throw new IllegalStateException("Failed to decode data domain key");
        }
        if (keyBytes.length != DATA_DOMAIN_KEY_LENGTH_BYTES) {
            throw new IllegalStateException(DATA_DOMAIN_KEY_INVALID_ERROR);
        }
    }

    private void validatePrefix(String keyPrefix) {
        if (!Strings.CS.endsWith(keyPrefix, "-")) {
            throw new IllegalStateException("api-key-prefix must end with a dash");
        }
    }

}
