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

package com.nvidia.boot.audit.listener;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.audit.event.BootAuditEvent;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;

@ExtendWith(OutputCaptureExtension.class)
class BootAuditEventListenerTest {

    private final BootAuditEventListener listener = new BootAuditEventListener();

    @Test
    void onBootAuditEventLogsPayloadToAudit(CapturedOutput output) {
        var payload = AuditEventPayload.builder()
                .operation("CREATE")
                .type("TASK")
                .actorId("actor")
                .objectId("obj-123")
                .state("CREATED")
                .summary("Created task")
                .build();
        var event = new BootAuditEvent(payload, null, BootAuditEventListenerTest.class);

        listener.onBootAuditEvent(event);

        assertThat(output).contains("[AUDIT]");
        assertThat(output).contains("CREATE");
        assertThat(output).contains("TASK");
        assertThat(output).contains("obj-123");
    }

    @Test
    void onBootAuditEventIncludesPayloadJson(CapturedOutput output) {
        var payload = AuditEventPayload.builder()
                .operation("UPDATE")
                .type("RESOURCE")
                .actorId("actor")
                .summary("Updated resource")
                .build();
        var event = new BootAuditEvent(payload, null, BootAuditEventListenerTest.class);

        listener.onBootAuditEvent(event);

        assertThat(output).contains("[AUDIT]");
        assertThat(output).contains("\"operation\":\"UPDATE\"");
        assertThat(output).contains("\"type\":\"RESOURCE\"");
    }
}
