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

public enum AuditOperation {

    CREATE_INSTANCE_REQUEST,
    UPDATE_INSTANCE_REQUEST,
    DELETE_INSTANCE_REQUEST,
    CANCEL_INSTANCE_REQUEST,
    CANCEL_PENDING_INSTANCE_REQUEST,
    CLOSE_EXPIRED_INSTANCE_REQUEST,
    REQUEST_STATE_TRANSITION_TO_ACTIVE,

    CREATE_INSTANCE,
    SHUTDOWN_INSTANCE,
    TERMINATE_INSTANCE,

    RUNNING_INSTANCE,

    STARTING_INSTANCE,

    TERMINATE_INSTANCE_REQUEST,
    UPDATE_INSTANCE_HEALTH,

    REGISTER_NEW_CLUSTER,

    MIGRATE_EXISTING_BART_CLUSTER_TO_NVCA,

    MIGRATE_EXISTING_BART_CLUSTER_PRIMARY_NCA_ID,

    RECONFIGURED_CLUSTER,

    DELETE_CLUSTER,

    UPDATE_CLUSTER,

    MIGRATE_INSTANCE_REQUEST_INSTANCE_TYPE,

    DB_CLEANUP_JOB,

    UPDATE_INSTANCE_EXPIRATION_TIME,

    TERMINATE_EXPIRED_RESERVED_BACKUP_INSTANCE,

    CREATE_TENANT_REGISTRATION,

    UPDATE_TENANT_REGISTRATION,

    DELETE_TENANT_REGISTRATION
}
