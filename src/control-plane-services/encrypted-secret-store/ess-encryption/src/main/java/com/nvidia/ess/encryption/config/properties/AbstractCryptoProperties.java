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
package com.nvidia.ess.encryption.config.properties;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonParser;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import java.text.ParseException;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.codec.binary.Base64;
import org.apache.commons.codec.binary.StringUtils;
import org.apache.commons.lang3.tuple.Pair;


/**
 * A Spring ConfigurationProperties class to load global key
 */
@Slf4j
public abstract class AbstractCryptoProperties {

    /**
     * Parses a base64-encoded JWK string into an OctetSequenceKey
     */
    protected OctetSequenceKey parseKey(String key) throws ParseException {
        if (org.apache.commons.lang3.StringUtils.isEmpty(key)) {
            throw new IllegalArgumentException("Master key cannot be null or blank");
        }

        var parsedCurrentKey = jwtInfo(convertVaultStr(key)).getRight();
        log.info("Loaded masterKey. Current kid: {}", parsedCurrentKey.getKeyID());

        return parsedCurrentKey;
    }

    /**
     * Parses a base64-encoded JSON array of JWKs into a sorted unmodifiable map of OctetSequenceKeys.
     * Duplicate map keys are handled by retaining only the last one
     */
    protected Map<String, OctetSequenceKey> parseAllKeys(String allKeys) throws ParseException {
        if (org.apache.commons.lang3.StringUtils.isEmpty(allKeys)) {
            throw new IllegalArgumentException("List of master key cannot be null or blank");
        }
        var parsedAllKeys = populateJsonArrayOfJwt(convertVaultStr(allKeys));
        if (parsedAllKeys.isEmpty()) {
            var msg = "allMasterKeys should not be empty";
            log.error(msg);
            throw new IllegalArgumentException(msg);
        }
        log.info("Loaded {} allMasterKeys. Kids: {}", parsedAllKeys.size(), parsedAllKeys.keySet());
        return parsedAllKeys;
    }

    // ---- utility functions to load JWT keys

    private String convertVaultStr(String str) {
        return StringUtils.newStringUtf8(Base64.decodeBase64(str));
    }

    private Pair<String, OctetSequenceKey> jwtInfo(String str) throws ParseException {
        var jwk = JWK.parse(str);
        return Pair.of(jwk.getKeyID(), jwk.toOctetSequenceKey());
    }

    private Map<String, OctetSequenceKey> populateJsonArrayOfJwt(String str) throws ParseException {
        JsonArray jsonArray = JsonParser.parseString(str).getAsJsonArray();

        var map = LinkedHashMap.<String, OctetSequenceKey>newLinkedHashMap(jsonArray.size());
        for (JsonElement el : jsonArray) {
            var jwt = jwtInfo(el.toString());
            map.put(jwt.getKey(), jwt.getRight());
        }
        return Collections.unmodifiableMap(map);
    }

}
