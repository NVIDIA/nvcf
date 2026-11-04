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
package com.nvidia.icms.util.audit;

public enum AuditState {

    CREATED_INSTANCE_REQUEST,
    UPDATED_INSTANCE_REQUEST,
    DELETED_INSTANCE_REQUEST,
    CANCELLED_INSTANCE_REQUEST,
    CANCELLED_PENDING_INSTANCE_REQUEST,
    CLOSED_EXPIRED_INSTANCE_REQUEST,
    CREATED_INSTANCE,
    SHUTDOWN_INSTANCE,
    TERMINATED_INSTANCE,

    RAN_INSTANCE,

    STARTED_INSTANCE,

    CREATED_CREDENTIAL_NOTUSED, //TODO Yury: Can it be deleted ?
    DELETED_CREDENTIAL_NOTUSED, //TODO Yury: Can it be deleted ?
    TERMINATED_INSTANCE_REQUEST,
    UPDATED_INSTANCE_HEALTH,
    REGISTERED_NEW_CLUSTER,

    DELETED_CLUSTER,

    UPDATED_CLUSTER,

    UPDATED_INSTANCE_EXPIRATION_TIME,

    TERMINATED_EXPIRED_RESERVED_BACKUP_INSTANCE,

    CREATED_TENANT_REGISTRATION,

    UPDATED_TENANT_REGISTRATION,

    DELETED_TENANT_REGISTRATION
}
