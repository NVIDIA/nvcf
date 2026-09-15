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

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.configuration.SynchronousAuditTestConfiguration;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.context.annotation.Import;

/**
 * Integration test to verify audit auto-configuration is loaded when the app starts.
 */
@SpringBootTest(classes = TestAuditApplication.class)
@Import(SynchronousAuditTestConfiguration.class)
@ExtendWith(OutputCaptureExtension.class)
class NvBootAuditIntegrationTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    @Autowired
    private AuditService auditService;

    @Test
    void auditServiceBeanIsAutoConfigured() {
        assertThat(auditService).isNotNull();
    }

    @Test
    void auditServiceProducesBuilderAndAudits() {
        var builder = auditService.auditEventPayloadBuilder()
                .operation("CREATE")
                .type("TASK")
                .objectId("test-123")
                .state("CREATED")
                .summary("Integration test audit");

        var payload = builder.build();

        assertThat(payload).isNotNull();
        assertThat(payload.getOperation()).isEqualTo("CREATE");
        assertThat(payload.getType()).isEqualTo("TASK");
        assertThat(payload.getObjectId()).isEqualTo("test-123");

        auditService.audit(builder);
    }

    @Test
    void auditEventLogsToStdout(CapturedOutput output) {
        auditService.audit(
                auditService.auditEventPayloadBuilder()
                        .operation("CREATE")
                        .type("TASK")
                        .objectId("obj-1")
                        .state("CREATED")
                        .summary("Test audit event"));

        assertThat(output).contains("[AUDIT]");
    }

    @Test
    void auditWithJsonBeforeAndJsonAfterLogsToStdout(CapturedOutput output) throws Exception {
        var before = JSON_MAPPER.readTree("{\"a\":1,\"b\":2}");
        var after = JSON_MAPPER.readTree("{\"a\":1,\"b\":3}");

        auditService.audit(
                auditService.auditEventPayloadBuilder()
                        .operation("UPDATE")
                        .type("TASK")
                        .objectId("obj-2")
                        .state("UPDATED")
                        .summary("Updated task")
                        .jsonBefore(before)
                        .jsonAfter(after));

        assertThat(output).contains("[AUDIT]");
        assertThat(output).contains("stateSummary");
        assertThat(output).contains("historySummary");
    }
}
