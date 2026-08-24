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

/**
 * SPI letting each module contribute the NATS JetStream streams + consumers it owns.
 *
 * <p>Spring auto-collects every {@code NatsStreamRegistrar} bean in the application context; the
 * {@link NatsStreamManager} iterates them on startup (strict path) and during periodic
 * re-validation (lenient path) and creates / re-creates every declared stream and consumer.</p>
 *
 * <p>This indirection keeps provider-specific stream names out of core code. Each module owns its
 * own stream names + subjects: the NVCA pair lives in {@link CoreNatsStreamRegistrar} (always
 * present), and a deployment module may contribute an additional pair (e.g. the non-BYOC streams,
 * only loaded when that module is on the classpath).</p>
 */
public interface NatsStreamRegistrar {

    /**
     * Returns the streams this registrar wants {@link NatsStreamManager} to manage. Implementations
     * should return a stable, immutable list; the manager treats the list as read-only.
     */
    List<NatsStreamDefinition> getStreamDefinitions();
}
