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

import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.apache.commons.lang3.Strings;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Service
public class ApiKeyParser {

    private static final String GROUP_BODY = "body";
    private static final String GROUP_HASH_PREFIX = "hashprefix";

    private final CredentialService credentialService;
    private final Pattern apiKeyPattern;

    public ApiKeyParser(
            CredentialService credentialService,
            @Value("${apikeys.key-prefix}") String prefix) {
        this.credentialService = credentialService;

        apiKeyPattern = Pattern.compile(
                "^" + prefix
                        + "(?<" + GROUP_BODY + ">[a-zA-Z0-9_-]{43})"
                        + "(?<" + GROUP_HASH_PREFIX + ">[a-zA-Z0-9_-]{21})$");
    }

    public String rawApiKeyToHash(String key) {
        if (key == null || key.isEmpty()) {
            throw new BadRequestException("api key empty");
        }
        Matcher matcher = apiKeyPattern.matcher(key);
        if (!matcher.matches()) {
            throw new BadRequestException("api key format invalid");
        }
        String keyBody = matcher.group(GROUP_BODY);
        String keyHashPrefix = matcher.group(GROUP_HASH_PREFIX);

        String fullKeyHash = credentialService.getKeyHash(keyBody);
        if (!Strings.CS.startsWith(fullKeyHash, keyHashPrefix)) {
            throw new BadRequestException("Invalid key");
        }

        return fullKeyHash;
    }

}
