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

import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.AuditUtils;
import java.util.ArrayDeque;
import java.util.Deque;
import java.util.List;
import org.junit.jupiter.api.Test;

class BootAuditEventTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    /** HMAC material for audit-chain tests (matches former {@link AuditEventPayloadTest} chain). */
    private static final String CHAIN_HMAC_KID = "test-kid";
    private static final byte[] CHAIN_HMAC_KEY = new byte[32];

    static {
        for (int i = 0; i < CHAIN_HMAC_KEY.length; i++) {
            CHAIN_HMAC_KEY[i] = (byte) (i + 1);
        }
    }

    /** HMAC material for event-level {@code BootAuditEvent} round-trip with top-level {@code hmac}. */
    private static final String SERIAL_HMAC_KID = "event-kid";
    private static final byte[] SERIAL_HMAC_KEY = new byte[32];

    static {
        for (int i = 0; i < SERIAL_HMAC_KEY.length; i++) {
            SERIAL_HMAC_KEY[i] = (byte) (0x40 + i);
        }
    }

    @Test
    void eventWrapsPayload() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .build();

        var event = new BootAuditEvent(payload, null, BootAuditEventTest.class);

        assertThat(event.getPayload()).isSameAs(payload);
        assertThat(event.getSource()).isSameAs(payload);
    }

    @Test
    void bootAuditEventRoundTripWithoutHmac() throws JacksonException {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .summary("no hmac")
                .build();

        var original = new BootAuditEvent(payload, null, BootAuditEventTest.class);
        var json = original.toJson();

        var restored = BootAuditEvent.fromJson(json);

        assertThat(restored.getHmac()).isNull();
        assertThat(restored.getPayload().getId()).isEqualTo(payload.getId());
        assertThat(restored.getPayload().getOperation()).isEqualTo("CREATE");
        assertThat(restored.getPayload().getType()).isEqualTo("TASK");
        assertThat(restored.getPayload().getSummary()).isEqualTo("no hmac");
    }

    @Test
    void bootAuditEventRoundTripWithHmac() throws JacksonException {
        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("RESOURCE")
                .actorId("actor")
                .summary("signed event")
                .build();

        var payloadJsonNode = BootAuditEvent.AUDIT_EVENT_JSON_MAPPER.readTree(payload.toJson());
        var eventHmac = AuditUtils.computeHmacFormatted(SERIAL_HMAC_KID, SERIAL_HMAC_KEY, payloadJsonNode);

        var original = new BootAuditEvent(payload, eventHmac, BootAuditEventTest.class);
        var json = original.toJson();

        var restored = BootAuditEvent.fromJson(json);

        assertThat(restored.getHmac()).isEqualTo(eventHmac);
        assertThat(restored.getPayload().getId()).isEqualTo(payload.getId());
        assertThat(restored.getPayload().getOperation()).isEqualTo("UPDATE");
        assertThat(restored.getPayload().getSummary()).isEqualTo("signed event");
    }

    /**
     * Verify Audit Event Chain - walk backwards from the latest {@code jsonAfter} by applying
     * {@code historySummary}; each step's HMAC must match that event payload's {@code hmacBefore}.
     */
    @Test
    void verifyAuditEventChainReverseHistoryMatchesHmacBefore() throws Exception {
        var nonExistent = JSON_MAPPER.readTree("{}");
        var initialized = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"INIT\",\"prop\":\"foo\"}");
        var created = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"CREATED\",\"prop\":\"bar\"}");
        var updated = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"UPDATED\",\"prop\":\"baz\"}");
        var deleted = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"DELETED\",\"prop\":\"baz\"}");

        var event1 = createSignedChainEvent(
                "INIT", "INITIALIZED", "Initialized ID_1", nonExistent, initialized);
        var event2 = createSignedChainEvent(
                "CREATE", "CREATED", "Created ID_1", initialized, created);
        var event3 = createSignedChainEvent(
                "UPDATE", "UPDATED", "Updated ID_1", created, updated);
        var event4 = createSignedChainEvent(
                "DELETE", "DELETED", "Deleted ID_1", updated, deleted);

        Deque<BootAuditEvent> events = new ArrayDeque<>();
        events.push(event1);
        events.push(event2);
        events.push(event3);
        events.push(event4);

        JsonNode currState = deleted;
        while (!events.isEmpty()) {
            BootAuditEvent curr = events.pop();
            JsonNode reverseDiff = JSON_MAPPER.readTree(curr.getPayload().getHistorySummary());
            currState = AuditJsonDiff.applyPatch(currState, reverseDiff);
            assertThat(AuditUtils.computeHmacFormatted(CHAIN_HMAC_KID, CHAIN_HMAC_KEY, currState))
                    .isEqualTo(curr.getPayload().getHmacBefore());
        }
        assertThat(currState).isEqualTo(nonExistent);
    }

    /**
     * Forward companion: apply {@code stateSummary} in chronological order; each step's HMAC must
     * match that event payload's {@code hmacAfter}.
     */
    @Test
    void verifyAuditEventChainForwardStateMatchesHmacAfter() throws Exception {
        var nonExistent = JSON_MAPPER.readTree("{}");
        var initialized = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"INIT\",\"prop\":\"foo\"}");
        var created = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"CREATED\",\"prop\":\"bar\"}");
        var updated = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"UPDATED\",\"prop\":\"baz\"}");
        var deleted = JSON_MAPPER.readTree("{\"id\":1,\"status\":\"DELETED\",\"prop\":\"baz\"}");

        var event1 = createSignedChainEvent(
                "INIT", "INITIALIZED", "Initialized ID_1", nonExistent, initialized);
        var event2 = createSignedChainEvent(
                "CREATE", "CREATED", "Created ID_1", initialized, created);
        var event3 = createSignedChainEvent(
                "UPDATE", "UPDATED", "Updated ID_1", created, updated);
        var event4 = createSignedChainEvent(
                "DELETE", "DELETED", "Deleted ID_1", updated, deleted);

        JsonNode currState = nonExistent;
        for (BootAuditEvent event : List.of(event1, event2, event3, event4)) {
            var payload = event.getPayload();
            JsonNode forwardDiff = JSON_MAPPER.readTree(payload.getStateSummary());
            currState = AuditJsonDiff.applyPatch(currState, forwardDiff);
            assertThat(AuditUtils.computeHmacFormatted(CHAIN_HMAC_KID, CHAIN_HMAC_KEY, currState))
                    .isEqualTo(payload.getHmacAfter());
        }
        assertThat(currState).isEqualTo(deleted);
    }

    private BootAuditEvent createSignedChainEvent(
            String operation,
            String state,
            String summary,
            JsonNode jsonBefore,
            JsonNode jsonAfter) {
        var payload = AuditEventPayload.signedBuilder(CHAIN_HMAC_KID, CHAIN_HMAC_KEY)
                .operation(operation)
                .type("type")
                .groupType("group")
                .actorId("john.doe")
                .actorLocation("216.235.112.22")
                .custom("max", Integer.MAX_VALUE)
                .custom("hello", "world")
                .state(state)
                .summary(summary)
                .jsonBefore(jsonBefore)
                .jsonAfter(jsonAfter)
                .build();
        return new BootAuditEvent(payload, null, BootAuditEventTest.class);
    }
}
