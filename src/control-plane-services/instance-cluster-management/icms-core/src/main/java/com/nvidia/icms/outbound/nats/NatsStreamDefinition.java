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

import jakarta.validation.constraints.NotBlank;

/**
 * Declarative description of a single JetStream stream and its companion consumer.
 *
 * <p>Used by {@link NatsStreamRegistrar} so that modules (core, non-BYOC, …) can declare the streams
 * they own without {@link NatsStreamManager} hard-coding any module-specific names. The manager
 * iterates {@link NatsStreamRegistrar} beans and creates / validates one stream + one consumer per
 * definition.</p>
 *
 * <p>Consumer name convention is {@code streamName + "Consumer"} (preserved verbatim from the
 * original {@code NatsStreamManager.createNatsConsumers()} implementation so existing NATS server
 * state continues to match).</p>
 *
 * @param streamName       the JetStream stream name (e.g. {@code CreateNvcaFunctionTaskStream}).
 * @param streamSubject    the broad subject pattern attached to the stream
 *                         (e.g. {@code Create.NVCA.>}).
 * @param consumerSubject  the filter subject the durable consumer subscribes to
 *                         (e.g. {@code Create.NVCA.*.*.*.*}).
 */
public record NatsStreamDefinition(
        @NotBlank String streamName,
        @NotBlank String streamSubject,
        @NotBlank String consumerSubject) {

    /** Convention used by {@code NatsStreamManager} since inception. */
    public String consumerName() {
        return streamName + "Consumer";
    }
}
