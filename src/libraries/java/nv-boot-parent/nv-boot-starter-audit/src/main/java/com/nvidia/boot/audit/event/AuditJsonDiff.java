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

package com.nvidia.boot.audit.event;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.github.fge.jsonpatch.JsonPatch;
import com.github.fge.jsonpatch.diff.JsonDiff;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

/**
 * Bridges Jackson 3 {@link JsonNode} trees to {@code json-patch} (Jackson 2 API) for summaries.
 */
final class AuditJsonDiff {

    private static final ObjectMapper JACKSON2 = new ObjectMapper();
    private static final JsonMapper JACKSON3 = JsonMapper.builder().build();

    private AuditJsonDiff() {}

    static String stateSummary(JsonNode before, JsonNode after) {
        return JsonDiff.asJson(toJackson2(before), toJackson2(after)).toString();
    }

    static String historySummary(JsonNode before, JsonNode after) {
        return JsonDiff.asJson(toJackson2(after), toJackson2(before)).toString();
    }

    static JsonNode applyPatch(JsonNode state, JsonNode patchDoc) {
        try {
            var patch = JsonPatch.fromJson(toJackson2(patchDoc));
            return fromJackson2(patch.apply(toJackson2(state)));
        } catch (Exception e) {
            throw new IllegalArgumentException("Failed to apply JSON patch", e);
        }
    }

    private static com.fasterxml.jackson.databind.JsonNode toJackson2(JsonNode node) {
        try {
            return JACKSON2.readTree(node.toString());
        } catch (Exception e) {
            throw new IllegalArgumentException("Failed to convert JSON for patch diff", e);
        }
    }

    private static JsonNode fromJackson2(com.fasterxml.jackson.databind.JsonNode node) {
        try {
            return JACKSON3.readTree(node.toString());
        } catch (Exception e) {
            throw new IllegalArgumentException("Failed to convert JSON patch result", e);
        }
    }
}
