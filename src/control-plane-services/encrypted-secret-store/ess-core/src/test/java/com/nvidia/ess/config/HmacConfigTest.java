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
package com.nvidia.ess.config;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.audit.AuditProperties;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;

class HmacConfigTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withConfiguration(AutoConfigurations.of(RefreshAutoConfiguration.class))
            .withUserConfiguration(HmacConfig.class);

    @Test
    void shouldNotRegisterAuditProperties_whenBothMissing() {
        contextRunner.run(context ->
                assertThat(context).doesNotHaveBean(AuditProperties.class));
    }

    @Test
    void shouldNotRegisterAuditProperties_whenOnlyKeysIsSet() {
        contextRunner
                .withPropertyValues("audit.hmac.keys=someEncodedKeystore")
                .run(context -> assertThat(context).doesNotHaveBean(AuditProperties.class));
    }

    @Test
    void shouldNotRegisterAuditProperties_whenOnlyKidIsSet() {
        contextRunner
                .withPropertyValues("audit.hmac.kid=someKid")
                .run(context -> assertThat(context).doesNotHaveBean(AuditProperties.class));
    }

    @Test
    void shouldNotRegisterAuditProperties_whenKeysIsLiteralFalse() {
        // @ConditionalOnProperty matches any value except "false" (case-insensitive). Setting
        // either property to literal false is therefore equivalent to leaving it unset.
        contextRunner
                .withPropertyValues("audit.hmac.keys=false", "audit.hmac.kid=someKid")
                .run(context -> assertThat(context).doesNotHaveBean(AuditProperties.class));
    }

    @Test
    void shouldNotRegisterAuditProperties_whenKidIsLiteralFalse() {
        contextRunner
                .withPropertyValues("audit.hmac.keys=someEncodedKeystore", "audit.hmac.kid=false")
                .run(context -> assertThat(context).doesNotHaveBean(AuditProperties.class));
    }

    @Test
    void shouldRegisterAuditProperties_whenBothAreSet() {
        contextRunner
                .withPropertyValues(
                        "audit.hmac.keys=someEncodedKeystore",
                        "audit.hmac.kid=someKid")
                .run(context -> {
                    // @RefreshScope registers two beans: the public proxy ("auditProperties") and
                    // the scoped target ("scopedTarget.auditProperties").
                    assertThat(context).hasBean("auditProperties");
                    var props = context.getBean("auditProperties", AuditProperties.class);
                    assertThat(props.getHmacKeys()).isEqualTo("someEncodedKeystore");
                    assertThat(props.getHmacKid()).isEqualTo("someKid");
                });
    }
}
