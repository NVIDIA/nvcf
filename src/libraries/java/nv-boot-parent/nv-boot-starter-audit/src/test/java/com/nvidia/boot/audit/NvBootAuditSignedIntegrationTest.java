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

package com.nvidia.boot.audit;

import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.configuration.SignedAuditPropertiesTestConfiguration;
import com.nvidia.boot.audit.configuration.SynchronousAuditTestConfiguration;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.context.annotation.Import;

/**
 * Integration test: Spring context with {@link AuditProperties} so {@link AuditService} emits
 * signed {@code BootAuditEvent} JSON (top-level {@code hmac} and optional payload HMAC fields).
 */
@SpringBootTest(classes = TestAuditApplication.class)
@Import({SynchronousAuditTestConfiguration.class, SignedAuditPropertiesTestConfiguration.class})
@ExtendWith(OutputCaptureExtension.class)
class NvBootAuditSignedIntegrationTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    private static final String AUDIT_PREFIX = "[AUDIT] ";

    @Autowired
    private AuditService auditService;

    @Test
    void signedAuditLogContainsTopLevelHmac(CapturedOutput output) throws Exception {
        auditService.audit(
                auditService.auditEventPayloadBuilder()
                        .operation("CREATE")
                        .type("TASK")
                        .objectId("signed-obj-1")
                        .state("CREATED")
                        .summary("Signed integration audit"));

        assertThat(output).contains(AUDIT_PREFIX);

        JsonNode event = readLastAuditEventJson(output);
        assertThat(event.hasNonNull("hmac")).isTrue();
        assertThat(event.get("hmac").asString())
                .startsWith("HMac-SHA3-512:" + SignedAuditPropertiesTestConfiguration.SIGNING_KID + ":");

        JsonNode payload = event.get("payload");
        assertThat(payload).isNotNull();
        assertThat(payload.get("operation").asString()).isEqualTo("CREATE");
        assertThat(payload.get("objectId").asString()).isEqualTo("signed-obj-1");
    }

    @Test
    void signedAuditWithJsonDiffContainsPayloadHmacs(CapturedOutput output) throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        auditService.audit(
                auditService.auditEventPayloadBuilder()
                        .operation("UPDATE")
                        .type("TASK")
                        .objectId("signed-obj-2")
                        .state("UPDATED")
                        .summary("Signed update with diff")
                        .jsonBefore(before)
                        .jsonAfter(after));

        assertThat(output).contains(AUDIT_PREFIX);

        JsonNode event = readLastAuditEventJson(output);
        assertThat(event.hasNonNull("hmac")).isTrue();

        JsonNode payload = event.get("payload");
        assertThat(payload.hasNonNull("hmacBefore")).isTrue();
        assertThat(payload.hasNonNull("hmacAfter")).isTrue();
        assertThat(payload.hasNonNull("stateSummary")).isTrue();
        assertThat(payload.hasNonNull("historySummary")).isTrue();

        assertThat(payload.get("hmacBefore").asString())
                .isEqualTo(
                        AuditUtils.computeHmacFormatted(
                                SignedAuditPropertiesTestConfiguration.SIGNING_KID,
                                signingKeyRaw(),
                                before));
        assertThat(payload.get("hmacAfter").asString())
                .isEqualTo(
                        AuditUtils.computeHmacFormatted(
                                SignedAuditPropertiesTestConfiguration.SIGNING_KID,
                                signingKeyRaw(),
                                after));
    }

    private static byte[] signingKeyRaw() {
        byte[] raw = new byte[32];
        for (int i = 0; i < raw.length; i++) {
            raw[i] = (byte) (i + 1);
        }
        return raw;
    }

    private static JsonNode readLastAuditEventJson(CapturedOutput output) throws Exception {
        String combined = output.getOut() + output.getErr();
        int idx = combined.lastIndexOf(AUDIT_PREFIX);
        assertThat(idx).as("expected at least one [AUDIT] line in captured output")
                .isGreaterThanOrEqualTo(0);
        String tail = combined.substring(idx + AUDIT_PREFIX.length()).trim();
        int nl = tail.indexOf('\n');
        if (nl > 0) {
            tail = tail.substring(0, nl);
        }
        return JSON_MAPPER.readTree(tail);
    }
}
