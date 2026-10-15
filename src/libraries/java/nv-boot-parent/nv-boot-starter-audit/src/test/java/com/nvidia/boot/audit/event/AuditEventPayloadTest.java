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

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.AuditUtils;
import java.util.Map;
import org.junit.jupiter.api.Test;

class AuditEventPayloadTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    @Test
    void builderProducesValidPayload() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("user-123")
                .subjectId("user-123")
                .objectId("task-456")
                .state("CREATED")
                .summary("Created task")
                .build();

        assertThat(payload.getId()).isNotNull();
        assertThat(payload.getTimestamp()).isNotNull();
        assertThat(payload.getMachineId()).isNotNull();
        assertThat(payload.getOperation()).isEqualTo("CREATE");
        assertThat(payload.getType()).isEqualTo("TASK");
        assertThat(payload.getActorId()).isEqualTo("user-123");
        assertThat(payload.getSubjectId()).isEqualTo("user-123");
        assertThat(payload.getObjectId()).isEqualTo("task-456");
        assertThat(payload.getState()).isEqualTo("CREATED");
        assertThat(payload.getSummary()).isEqualTo("Created task");
        assertThat(payload.getData()).isEmpty();
    }

    @Test
    void subjectIdDefaultsToActorIdWhenBlank() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("user-123")
                .subjectId("")
                .build();

        assertThat(payload.getSubjectId()).isEqualTo("user-123");
    }

    @Test
    void subjectIdDefaultsToActorIdWhenNull() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("user-123")
                .build();

        assertThat(payload.getSubjectId()).isEqualTo("user-123");
    }

    @Test
    void customDataIsIncludedInPayload() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .custom("key1", "value1")
                .custom("key2", 42)
                .build();

        assertThat(payload.getData()).containsEntry("key1", "value1");
        assertThat(payload.getData()).containsEntry("key2", 42);
    }

    @Test
    void dataMapMergesWithExistingCustomEntries() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .custom("key1", "value1")
                .data(Map.of("key2", "value2"))
                .build();

        assertThat(payload.getData()).containsEntry("key1", "value1");
        assertThat(payload.getData()).containsEntry("key2", "value2");
    }

    @Test
    void dataMapOverwritesSameKeyAsEarlierCustom() {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .custom("key1", "first")
                .data(Map.of("key1", "second"))
                .build();

        assertThat(payload.getData()).containsEntry("key1", "second");
    }

    @Test
    void toJsonProducesValidJson() throws Exception {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("user-123")
                .build();

        var json = payload.toJson();
        var node = JSON_MAPPER.readTree(json);

        assertThat(node.has("id")).isTrue();
        assertThat(node.has("timestamp")).isTrue();
        assertThat(node.has("machineId")).isTrue();
        assertThat(node.get("operation").asString()).isEqualTo("CREATE");
        assertThat(node.get("type").asString()).isEqualTo("TASK");
        assertThat(node.get("actorId").asString()).isEqualTo("user-123");
    }

    @Test
    void jsonBeforeAndJsonAfterProduceStateAndHistorySummary() throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getStateSummary()).isNotNull();
        assertThat(payload.getHistorySummary()).isNotNull();
        assertThat(payload.getStateSummary()).contains("b");
        assertThat(payload.getHistorySummary()).contains("b");
    }

    @Test
    void jsonBeforeOnlyProducesNoStateOrHistorySummary() throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .build();

        assertThat(payload.getStateSummary()).isNull();
        assertThat(payload.getHistorySummary()).isNull();
    }

    @Test
    void jsonAfterOnlyProducesNoStateOrHistorySummary() throws Exception {
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonAfter(after)
                .build();

        assertThat(payload.getStateSummary()).isNull();
        assertThat(payload.getHistorySummary()).isNull();
    }

    @Test
    void jsonBeforeAndJsonAfterWithAddedFieldProducesDiff() throws Exception {
        var before = JSON_MAPPER.readTree("{\"name\":\"old\"}");
        var after = JSON_MAPPER.readTree("{\"name\":\"old\",\"newField\":\"value\"}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getStateSummary()).isNotNull();
        assertThat(payload.getHistorySummary()).isNotNull();
        assertThat(payload.getStateSummary()).contains("add");
        assertThat(payload.getStateSummary()).contains("newField");
    }

    @Test
    void jsonBeforeAndJsonAfterWithRemovedFieldProducesDiff() throws Exception {
        var before = JSON_MAPPER.readTree("{\"name\":\"old\",\"removed\":\"value\"}");
        var after = JSON_MAPPER.readTree("{\"name\":\"old\"}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getStateSummary()).isNotNull();
        assertThat(payload.getHistorySummary()).isNotNull();
        assertThat(payload.getStateSummary()).contains("remove");
    }

    @Test
    void jsonBeforeAndJsonAfterHistorySummaryRevertsStateSummary() throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getStateSummary()).isNotEqualTo(payload.getHistorySummary());
        assertThat(payload.getStateSummary()).contains("replace");
        assertThat(payload.getHistorySummary()).contains("replace");
    }

    @Test
    void builderIsFluent() {
        var builder = AuditEventPayload.builder();

        var sameBuilder = builder.operation("CREATE")
                .type("TASK")
                .actorId("user-1")
                .actorLocation("127.0.0.1")
                .subjectId("user-1")
                .subjectLocation("127.0.0.1")
                .objectId("obj-1")
                .objectLocation("loc")
                .groupType("group")
                .state("CREATED")
                .summary("Created");

        assertThat(sameBuilder).isSameAs(builder);
        var payload = builder.build();
        assertThat(payload.getOperation()).isEqualTo("CREATE");
    }

    private static final String HMAC_KID = "test-kid";
    private static final byte[] HMAC_KEY = new byte[32];

    static {
        for (int i = 0; i < HMAC_KEY.length; i++) {
            HMAC_KEY[i] = (byte) (i + 1);
        }
    }

    @Test
    void signedBuilderSetsHmacBeforeAndHmacAfterWhenBothJsonNodesPresent() throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        var payload = AuditEventPayload.signedBuilder(HMAC_KID, HMAC_KEY)
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getHmacBefore())
                .isEqualTo(AuditUtils.computeHmacFormatted(HMAC_KID, HMAC_KEY, before));
        assertThat(payload.getHmacAfter())
                .isEqualTo(AuditUtils.computeHmacFormatted(HMAC_KID, HMAC_KEY, after));
        assertThat(payload.getStateSummary()).isNotNull();
        assertThat(payload.getHistorySummary()).isNotNull();
    }

    @Test
    void signedBuilderSetsOnlyHmacBeforeWhenJsonAfterAbsent() throws Exception {
        var before = JSON_MAPPER.readTree("{\"x\":true}");

        var payload = AuditEventPayload.signedBuilder(HMAC_KID, HMAC_KEY)
                .operation("DELETE")
                .type("ROW")
                .actorId("actor")
                .jsonBefore(before)
                .build();

        assertThat(payload.getHmacBefore())
                .isEqualTo(AuditUtils.computeHmacFormatted(HMAC_KID, HMAC_KEY, before));
        assertThat(payload.getHmacAfter()).isNull();
    }

    @Test
    void signedBuilderSetsOnlyHmacAfterWhenJsonBeforeAbsent() throws Exception {
        var after = JSON_MAPPER.readTree("{\"y\":2}");

        var payload = AuditEventPayload.signedBuilder(HMAC_KID, HMAC_KEY)
                .operation("CREATE")
                .type("ROW")
                .actorId("actor")
                .jsonAfter(after)
                .build();

        assertThat(payload.getHmacBefore()).isNull();
        assertThat(payload.getHmacAfter())
                .isEqualTo(AuditUtils.computeHmacFormatted(HMAC_KID, HMAC_KEY, after));
    }

    @Test
    void unsignedBuilderLeavesHmacFieldsNullEvenWithJsonBeforeAndAfter() throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1}");
        var after = JSON_MAPPER.readTree("{\"a\":2}");

        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("TASK")
                .actorId("actor")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getHmacBefore()).isNull();
        assertThat(payload.getHmacAfter()).isNull();
    }

    @Test
    void formattedHmacPatternRejectsMalformedStrings() throws Exception {
        var p = AuditEventPayload.FORMATTED_HMAC_PATTERN;
        assertThat(p.matcher("").matches()).isFalse();
        assertThat(p.matcher("only-one").matches()).isFalse();
        assertThat(p.matcher("a:b").matches()).isFalse();
        assertThat(p.matcher(":b:c").matches()).isFalse();
        assertThat(p.matcher("a::c").matches()).isFalse();
        assertThat(p.matcher("a:b:c:d").matches()).isFalse();
        assertThat(p.matcher("a:b:c").matches()).isTrue();
        assertThat(AuditUtils.computeHmacFormatted(HMAC_KID, HMAC_KEY, JSON_MAPPER.readTree("{}")))
                .matches(p);
    }

    @Test
    void fromJsonRoundTripPreservesIdentityFields() {
        var payload = AuditEventPayload.signedBuilder("kid-2", HMAC_KEY)
                .operation("PATCH")
                .type("DOC")
                .actorId("a")
                .summary("s")
                .build();

        var json = payload.toJson();
        var restored = AuditEventPayload.fromJson(json);

        assertThat(restored.getId()).isEqualTo(payload.getId());
        assertThat(restored.getTimestamp()).isEqualTo(payload.getTimestamp());
        assertThat(restored.getMachineId()).isEqualTo(payload.getMachineId());
        assertThat(restored.getOperation()).isEqualTo("PATCH");
        assertThat(restored.getType()).isEqualTo("DOC");
        assertThat(restored.getActorId()).isEqualTo("a");
        assertThat(restored.getSummary()).isEqualTo("s");
    }
}
