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
package com.nvidia.nvct.util;

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.CANCELED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_QUEUED_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION;

import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Collections;
import java.util.EnumSet;
import java.util.Set;
import java.util.UUID;
import lombok.experimental.UtilityClass;

@UtilityClass
public final class NvctConstants {

    // Super-Admin Scopes
    public static final String ADMIN_SCOPE_LAUNCH_TASK = "admin:launch_task";
    public static final String ADMIN_SCOPE_LIST_TASKS = "admin:list_tasks";
    public static final String ADMIN_SCOPE_TASK_DETAILS = "admin:task_details";
    public static final String ADMIN_SCOPE_CANCEL_TASK = "admin:cancel_task";
    public static final String ADMIN_SCOPE_DELETE_TASK = "admin:delete_task";
    public static final String ADMIN_SCOPE_LIST_EVENTS = "admin:list_events";
    public static final String ADMIN_SCOPE_LIST_RESULTS = "admin:list_results";
    public static final String ADMIN_SCOPE_UPDATE_SECRETS = "admin:update_secrets";
    public static final Set<String> SUPER_ADMIN_SCOPES = Set.of(ADMIN_SCOPE_LAUNCH_TASK,
                                                                ADMIN_SCOPE_LIST_TASKS,
                                                                ADMIN_SCOPE_TASK_DETAILS,
                                                                ADMIN_SCOPE_CANCEL_TASK,
                                                                ADMIN_SCOPE_DELETE_TASK,
                                                                ADMIN_SCOPE_LIST_EVENTS,
                                                                ADMIN_SCOPE_LIST_RESULTS,
                                                                ADMIN_SCOPE_UPDATE_SECRETS);

    // Account-Admin Scopes
    public static final String SCOPE_LAUNCH_TASK = "launch_task";
    public static final String SCOPE_LIST_TASKS = "list_tasks";
    public static final String SCOPE_TASK_DETAILS = "task_details";
    public static final String SCOPE_CANCEL_TASK = "cancel_task";
    public static final String SCOPE_DELETE_TASK = "delete_task";
    public static final String SCOPE_LIST_EVENTS = "list_events";
    public static final String SCOPE_LIST_RESULTS = "list_results";
    public static final String SCOPE_UPDATE_SECRETS = "update_secrets";
    public static final Set<String> ACCOUNT_ADMIN_SCOPES = Set.of(SCOPE_LAUNCH_TASK,
                                                                  SCOPE_LIST_TASKS,
                                                                  SCOPE_TASK_DETAILS,
                                                                  SCOPE_CANCEL_TASK,
                                                                  SCOPE_DELETE_TASK,
                                                                  SCOPE_LIST_EVENTS,
                                                                  SCOPE_LIST_RESULTS,
                                                                  SCOPE_UPDATE_SECRETS);

    // Base64 encoded empty JSON array [].
    public static final String DEFAULT_CONTAINER_ENV = Base64.getEncoder()
            .encodeToString("[]".getBytes(StandardCharsets.UTF_8));

    public static final String REQUEST_URI = "requestUri";
    public static final String REMOTE_ADDRESS = "remoteAddress";
    public static final String HTTP_METHOD = "httpMethod";
    public static final String UNKNOWN = "UNKNOWN";
    public static final String ACTOR_ID_DELIMITER = "_";
    public static final String NGC_API_KEY = "NGC_API_KEY";

    public static final String TASK_OBJECT_LOCATION = "urn:nvcf:cassandra:nvct:tasks";

    public static final String GRP_TYPE_TASK_MANAGEMENT = "TASK_MANAGEMENT";

    public static final String OPER_CREATE_TASK = "CREATE_TASK";
    public static final String OPER_CANCEL_TASK = "CANCEL_TASK";
    public static final String OPER_DELETE_TASK = "DELETE_TASK";
    public static final String OPER_UPDATE_TASK = "UPDATE_TASK";

    public static final String SUMMARY_CREATE_TASK = "Created task '%s' for account '%s'";
    public static final String SUMMARY_CANCEL_TASK = "Canceled task '%s'";
    public static final String SUMMARY_DELETE_TASK = "Deleted task '%s'";
    public static final String SUMMARY_UPDATE_TASK = "Updated task '%s' status to '%s'";
    public static final String SUMMARY_UPDATE_TASK_HEARTBEAT = "Updated task '%s' heartbeat";
    public static final String SUMMARY_UPDATE_TASK_ENTITY = "Updated task '%s' entity";

    public static final String STATE_CREATED = "CREATED";
    public static final String STATE_CANCELED = "CANCELED";
    public static final String STATE_DELETED = "DELETED";
    public static final String STATE_UPDATED = "UPDATED";

    public static final String NCA_ID = "nca_id";
    public static final String TASK_ID = "task_id";
    public static final String TASK_STATUS = "task_status";
    public static final String UPDATE_TYPE = "update_type";
    public static final String UPDATE_TYPE_STATUS = "status";
    public static final String UPDATE_TYPE_HEARTBEAT = "heartbeat";
    public static final String UPDATE_TYPE_ENTITY = "entity";

    public static final String ENC_KEY_NAME = "current-kid";

    public static final String SPAN_TAG_TASK_ID = "task_id";
    public static final String SPAN_TAG_TASK_NAME = "task_name";
    public static final String SPAN_TAG_NCA_ID = "nca_id";
    public static final String SPAN_TAG_ACCOUNT_NAME = "account_name";
    public static final String SPAN_TAG_TASK_STATUS = "task_status";

    public static final int MAX_TAGS_COUNT = 64;
    public static final int MAX_TAG_LENGTH = 128;
    public static final int MAX_DESCRIPTION_LENGTH = 256;
    public static final String NAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\-_]*$";

    public static final UUID UUID_WILDCARD = new UUID(0, 0);

    public static final String DEFAULT_PAGINATION_LIMIT = "10";
    public static final String MESG_INVALID_CURSOR = "Invalid cursor: '%s'";

    // Metrics - Meter names
    public static final String METER_TASK_RUNNING = "nvct.task.running";
    public static final String METER_TASK_SUCCESS = "nvct.task.success";
    public static final String METER_TASK_ERROR = "nvct.task.error";

    // Metrics - Tag names
    public static final String TAG_NCA_ID = "nca_id";
    public static final String TAG_TASK_ID = "task_id";
    public static final String TAG_ORG_NAME = "account_name";
    public static final String TAG_ERROR_SOURCE = "error_source";

    public static final String ESS_NAMESPACE = "nvcf";

    public static final Set<TaskStatus> TERMINAL_TASK_STATUSES =
            Collections.unmodifiableSet(EnumSet.of(CANCELED,
                                                   COMPLETED,
                                                   ERRORED,
                                                   EXCEEDED_MAX_RUNTIME_DURATION,
                                                   EXCEEDED_MAX_QUEUED_DURATION));

    public static final int MAX_BUFFER_LIMIT = 10 * 1024 * 1024;

    public static final int MAX_SECRET_VALUE_LENGTH = 32768;
    public static final int MAX_SECRET_NAME_LENGTH = 48;


    // Hostname Syntax - https://en.wikipedia.org/wiki/Hostname#Syntax
    // public static final String HOSTNAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\-.]*$";
    // public static final String HOSTNAME_REGEX = "^[A-Za-z0-9][A-Za-z0-9-.]*\\.\\D{2,4}$";
    public static final String HOSTNAME_REGEX =
            "(?=^.{4,253}$)(^((?!-)[a-zA-Z0-9-]{1,63}(?<!-)\\.)+[a-zA-Z0-9]{2,63}$)";
    public static final int MAX_HOSTNAME_LENGTH = 253;
    public static final String TAG_REGEX = "[a-zA-Z0-9\\-_:=]+";

    public static final int BATCH_SIZE = 2000;
}
