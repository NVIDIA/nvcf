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

import static com.nvidia.apikeys.config.hmac.HmacEncoder.HMAC_SHA_3_256;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.ApiKeyException;
import com.nvidia.apikeys.config.hmac.HmacEncoder;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.validators.NakPropertiesValidator;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import java.security.SecureRandom;
import java.util.Base64;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.JdkIdGenerator;

@Slf4j
@Service
public class CredentialService {

    // see reasoning in SDD https://docs.google.com/document/d/1oWg9jPlGzcuerpjE4P-pWHXpVGDDUcCSlld6Vd7Gaqs/edit#heading=h.f2a6a9vw0uei
    public static final int API_KEY_ENTROPY_BYTES = 32; // 256 bit
    public static final int KEY_ID_SUFFIX_LENGTH_CHARS = 21;
    public static final int KEY_HINT_LENGHT = 3;
    public static final String KEY_SUFFIX_MASK = "**********";
    private static final int MAX_ATTEMPTS_TO_GENERATE_KEY_ID = 50;

    private final HmacEncoder hmacEncoder;
    private final JdkIdGenerator jdkIdGenerator;
    private final Base64.Encoder b64urlEncoder;
    private final Base64.Decoder b64urlDecoder;
    private final KeyRepository keyRepository;

    private final byte[] dataDomainKey;
    private final String keyPrefix;

    private static final SecureRandom secureRandom = new SecureRandom();

    public CredentialService(
            HmacEncoder hmacEncoder,
            NakPropertiesValidator nakPropertiesValidator,
            NakProperties nakProperties,
            JdkIdGenerator jdkIdGenerator,
            KeyRepository keyRepository) {
        this.hmacEncoder = hmacEncoder;
        this.jdkIdGenerator = jdkIdGenerator;
        this.keyRepository = keyRepository;
        b64urlEncoder = Base64.getUrlEncoder().withoutPadding();
        b64urlDecoder = Base64.getUrlDecoder();

        NakProperties validConfig = nakPropertiesValidator.validate(nakProperties);

        this.dataDomainKey = b64urlDecoder.decode(validConfig.getDataDomainKey());
        this.keyPrefix = validConfig.getKeyPrefix();
    }

    public GeneratedKeyVo generateApiKey() {
        for (int i = 0; i < MAX_ATTEMPTS_TO_GENERATE_KEY_ID; i++) {
            GeneratedKeyVo generatedKeyVo = generateKeyInternal();
            if (keyRepository.findByKeyHash(generatedKeyVo.getKeyHash()).isEmpty()) {
                return generatedKeyVo;
            }
        }

        log.error("failed to generate unique key hash after {} attempts",
                  MAX_ATTEMPTS_TO_GENERATE_KEY_ID);
        throw new ApiKeyException("Failed to generate unique key, please retry", null);
    }

    private GeneratedKeyVo generateKeyInternal() {
        byte[] apiKeyBytes = generateApiKeyBytes();
        String keyHash = b64urlEncoder.encodeToString(apiKeyToHash(apiKeyBytes));
        String apiKeyString = b64urlEncoder.encodeToString(apiKeyBytes);
        String keyIdSuffix = keyHash.substring(0, KEY_ID_SUFFIX_LENGTH_CHARS);
        return GeneratedKeyVo.builder()
                .formattedApiKey(keyPrefix + apiKeyString + keyIdSuffix)
                .keyHash(keyHash)
                .keyId(jdkIdGenerator.generateId().toString())
                .keySuffix(keyPrefix + KEY_SUFFIX_MASK +
                           StringUtils.right(keyIdSuffix, KEY_HINT_LENGHT))
                .build();
    }

    public String getKeyHash(String apiKeyBody) {
        byte[] apiKeyBytes = b64urlDecoder.decode(apiKeyBody);
        byte[] hashBytes = apiKeyToHash(apiKeyBytes);
        return b64urlEncoder.encodeToString(hashBytes);
    }

    private byte[] generateApiKeyBytes() {
        byte[] apiKeyBytes = new byte[API_KEY_ENTROPY_BYTES];
        secureRandom.nextBytes(apiKeyBytes);
        return apiKeyBytes;
    }

    private byte[] apiKeyToHash(byte[] apiKeyBytes) {
        try {
            return hmacEncoder.hmac(HMAC_SHA_3_256, dataDomainKey, apiKeyBytes);
        } catch (Exception e) {
            throw new ApiKeyException("failed to process key", e);
        }
    }

}
