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

package com.nvidia.boot.audit.configuration;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ArrayNode;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.boot.audit.AuditProperties;
import java.util.Base64;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;

/**
 * Supplies {@link AuditProperties} for integration tests that exercise HMAC-signed audit events.
 */
@TestConfiguration
public class SignedAuditPropertiesTestConfiguration {

    /** {@link AuditProperties#setHmacKid} value; matches the key entry in the generated store. */
    public static final String SIGNING_KID = "integration-audit-kid";

    private static final byte[] SIGNING_KEY_RAW = new byte[32];

    static {
        for (int i = 0; i < SIGNING_KEY_RAW.length; i++) {
            SIGNING_KEY_RAW[i] = (byte) (i + 1);
        }
    }

    @Bean
    public AuditProperties auditProperties(JsonMapper jsonMapper) throws JacksonException {
        ArrayNode keys = jsonMapper.createArrayNode();
        ObjectNode entry = jsonMapper.createObjectNode();
        entry.put("kid", SIGNING_KID);
        entry.put("key", Base64.getEncoder().encodeToString(SIGNING_KEY_RAW));
        keys.add(entry);
        ObjectNode root = jsonMapper.createObjectNode();
        root.set("keys", keys);
        var props = new AuditProperties();
        props.setHmacKid(SIGNING_KID);
        props.setHmacKeys(Base64.getEncoder().encodeToString(jsonMapper.writeValueAsBytes(root)));
        return props;
    }
}
