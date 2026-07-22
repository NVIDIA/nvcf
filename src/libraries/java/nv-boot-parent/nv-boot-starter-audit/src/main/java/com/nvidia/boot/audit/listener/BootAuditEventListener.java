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

import static org.slf4j.Logger.ROOT_LOGGER_NAME;

import com.nvidia.boot.audit.event.BootAuditEvent;
import org.slf4j.LoggerFactory;
import org.springframework.context.event.EventListener;
import org.springframework.scheduling.annotation.Async;
import org.springframework.scheduling.annotation.EnableAsync;
import org.springframework.stereotype.Component;

/**
 * Async listener for {@link BootAuditEvent}. Logs the full event JSON to the root audit logger
 * namespace.
 */
@EnableAsync
@Component
public class BootAuditEventListener {

    @EventListener
    @Async
    public void onBootAuditEvent(BootAuditEvent event) {
        var className = event.getSourceClassName();
        var contextLogger = LoggerFactory.getLogger(ROOT_LOGGER_NAME + "." + className);
        contextLogger.info("[AUDIT] " + event.toJson());
    }
}
