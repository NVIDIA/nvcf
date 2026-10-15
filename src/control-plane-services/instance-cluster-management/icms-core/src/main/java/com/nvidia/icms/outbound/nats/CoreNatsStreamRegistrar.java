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
package com.nvidia.icms.outbound.nats;

import java.util.List;
import org.springframework.stereotype.Component;

/**
 * NVCA JetStream streams that ICMS-core always needs. Lives in core because the NVCA
 * pipeline is part of the always-on, non BYOC deployment surface.
 *
 * <p>Stream + subject string values are byte-identical to the constants previously declared inline
 * in {@code NatsStreamManager} so no NATS server-side state changes are required by R8.</p>
 */
@Component
public class CoreNatsStreamRegistrar implements NatsStreamRegistrar {

    public static final String CREATE_NVCA_STREAM_NAME = "CreateNvcaFunctionTaskStream";
    public static final String TERMINATE_NVCA_STREAM_NAME = "TerminateNvcaStream";

    private static final String CREATE_NVCA_STREAM_SUBJECT = "Create.NVCA.>";
    private static final String CREATE_NVCA_CONSUMER_SUBJECT = "Create.NVCA.*.*.*.*";
    private static final String TERMINATE_NVCA_STREAM_SUBJECT = "Terminate.NVCA.>";
    private static final String TERMINATE_NVCA_CONSUMER_SUBJECT = "Terminate.NVCA.*";

    private static final List<NatsStreamDefinition> DEFINITIONS = List.of(
            new NatsStreamDefinition(
                    CREATE_NVCA_STREAM_NAME,
                    CREATE_NVCA_STREAM_SUBJECT,
                    CREATE_NVCA_CONSUMER_SUBJECT),
            new NatsStreamDefinition(
                    TERMINATE_NVCA_STREAM_NAME,
                    TERMINATE_NVCA_STREAM_SUBJECT,
                    TERMINATE_NVCA_CONSUMER_SUBJECT));

    @Override
    public List<NatsStreamDefinition> getStreamDefinitions() {
        return DEFINITIONS;
    }
}
