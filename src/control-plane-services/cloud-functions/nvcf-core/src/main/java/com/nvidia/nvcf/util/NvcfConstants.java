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
package com.nvidia.nvcf.util;

import com.nvidia.nvcf.persistence.function.entity.Protocol;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Set;
import java.util.UUID;
import lombok.experimental.UtilityClass;
import org.apache.commons.lang3.StringUtils;

@UtilityClass
public final class NvcfConstants {

    public static final String METADATA_KEY_NVCF_ASSET_DESC = "nvcf-asset-description";
    public static final String DEFAULT_ASSET_DESCRIPTION = "Description not available";

    // Super-Admin Scopes
    public static final String ADMIN_SCOPE_ACCOUNT_SETUP = "account_setup";
    public static final String ADMIN_SCOPE_AUTHORIZE_CLIENTS = "admin:authorize_clients";
    public static final String ADMIN_SCOPE_REGISTER_FUNCTION = "admin:register_function";
    public static final String ADMIN_SCOPE_UPDATE_FUNCTION = "admin:update_function";
    public static final String ADMIN_SCOPE_DELETE_FUNCTION = "admin:delete_function";
    public static final String ADMIN_SCOPE_DEPLOY_FUNCTION = "admin:deploy_function";
    public static final String ADMIN_SCOPE_LIST_FUNCTIONS = "admin:list_functions";
    public static final String ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS = "admin:list_functions_details";
    public static final String ADMIN_SCOPE_QUEUE_DETAILS = "admin:queue_details";
    public static final String ADMIN_SCOPE_INVOKE_FUNCTION = "admin:invoke_function";
    public static final String ADMIN_SCOPE_UPDATE_SECRETS = "admin:update_secrets";
    public static final String ADMIN_SCOPE_MANAGE_TELEMETRIES = "admin:manage_telemetries";
    public static final String SCOPE_AUTOSCALER_AUTH = "autoscaler:auth";
    public static final String ADMIN_SCOPE_MANAGE_REGISTRY_CREDENTIALS =
            "admin:manage_registry_credentials";
    public static final Set<String> SUPER_ADMIN_SCOPES = Set.of(ADMIN_SCOPE_ACCOUNT_SETUP,
                                                                ADMIN_SCOPE_AUTHORIZE_CLIENTS,
                                                                ADMIN_SCOPE_REGISTER_FUNCTION,
                                                                ADMIN_SCOPE_UPDATE_FUNCTION,
                                                                ADMIN_SCOPE_DELETE_FUNCTION,
                                                                ADMIN_SCOPE_LIST_FUNCTIONS,
                                                                ADMIN_SCOPE_DEPLOY_FUNCTION,
                                                                ADMIN_SCOPE_QUEUE_DETAILS,
                                                                ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS,
                                                                ADMIN_SCOPE_MANAGE_TELEMETRIES,
                                                                ADMIN_SCOPE_MANAGE_REGISTRY_CREDENTIALS);

    // Account-Admin Scopes
    public static final String SCOPE_AUTHORIZE_CLIENTS = "authorize_clients";
    public static final String SCOPE_REGISTER_FUNCTION = "register_function";
    public static final String SCOPE_UPDATE_FUNCTION = "update_function";
    public static final String SCOPE_DELETE_FUNCTION = "delete_function";
    public static final String SCOPE_DEPLOY_FUNCTION = "deploy_function";
    public static final String SCOPE_LIST_FUNCTIONS = "list_functions";
    public static final String SCOPE_LIST_FUNCTIONS_DETAILS = "list_functions_details";
    public static final String SCOPE_INVOKE_FUNCTION = "invoke_function";
    public static final String SCOPE_LLM_CHECK_INVOCATION = "llm:check_invocation";
    public static final String SCOPE_LLM_CHECK_WORKER = "llm:check_worker";
    public static final String SCOPE_QUEUE_DETAILS = "queue_details";
    public static final String SCOPE_UPDATE_SECRETS = "update_secrets";
    public static final String SCOPE_MANAGE_TELEMETRIES = "manage_telemetries";
    public static final String SCOPE_MANAGE_REGISTRY_CREDENTIALS = "manage_registry_credentials";

    // Account-User Scopes
    public static final Set<String> ACCOUNT_USER_SCOPES = Set.of(SCOPE_INVOKE_FUNCTION,
                                                                 SCOPE_QUEUE_DETAILS,
                                                                 SCOPE_LIST_FUNCTIONS);

    // Base64 encoded empty JSON array [].
    public static final String DEFAULT_CONTAINER_ENV = Base64.getEncoder()
            .encodeToString("[]".getBytes(StandardCharsets.UTF_8));

    public static final String REQUEST_URI = "requestUri";
    public static final String REMOTE_ADDRESS = "remoteAddress";
    public static final String HTTP_METHOD = "httpMethod";
    public static final String UNKNOWN = "UNKNOWN";
    public static final String ACTOR_ID_DELIMITER = "_";

    public static final String ACCOUNT_OBJECT_LOCATION = "urn:nvcf:cassandra:nvcf:accounts";
    public static final String CLIENT_OBJECT_LOCATION = "urn:nvcf:cassandra:nvcf:clients";
    public static final String FUNCTION_OBJECT_LOCATION = "urn:nvcf:cassandra:nvcf:functions";
    public static final String DEPLOYMENT_OBJECT_LOCATION = "urn:nvcf:cassandra:nvcf:functions_deployment";
    public static final String REGISTRY_CREDENTIAL_OBJECT_LOCATION =
            "urn:nvcf:cassandra:nvcf:registry_credentials_by_account";

    public static final String GRP_TYPE_ACCOUNT_MANAGEMENT = "ACCOUNT_MANAGEMENT";
    public static final String GRP_TYPE_FUNCTION_MANAGEMENT = "FUNCTION_MANAGEMENT";
    public static final String GRP_TYPE_FUNCTION_DEPLOYMENT = "FUNCTION_DEPLOYMENT";
    public static final String GRP_TYPE_REGISTRY_CREDENTIAL_MANAGEMENT =
            "REGISTRY_CREDENTIAL_MANAGEMENT";

    public static final String OPER_CREATE_ACCOUNT = "CREATE_ACCOUNT";
    public static final String OPER_UPDATE_ACCOUNT = "UPDATE_ACCOUNT";
    public static final String OPER_DELETE_ACCOUNT = "DELETE_ACCOUNT";
    public static final String OPER_CREATE_FUNCTION = "CREATE_FUNCTION";
    public static final String OPER_UPDATE_FUNCTION = "UPDATE_FUNCTION";
    public static final String OPER_DELETE_FUNCTION = "DELETE_FUNCTION";
    public static final String OPER_CREATE_FUNCTION_DEPLOYMENT = "CREATE_FUNCTION_DEPLOYMENT";
    public static final String OPER_UPDATE_FUNCTION_DEPLOYMENT = "UPDATE_FUNCTION_DEPLOYMENT";
    public static final String OPER_DELETE_FUNCTION_DEPLOYMENT = "DELETE_FUNCTION_DEPLOYMENT";
    public static final String OPER_CREATE_REGISTRY_CREDENTIAL = "CREATE_REGISTRY_CREDENTIAL";
    public static final String OPER_UPDATE_REGISTRY_CREDENTIAL = "UPDATE_REGISTRY_CREDENTIAL";
    public static final String OPER_DELETE_REGISTRY_CREDENTIAL = "DELETE_REGISTRY_CREDENTIAL";

    public static final String SUMMARY_CREATE_ACCOUNT = "Created account '%s' for '%s'";
    public static final String SUMMARY_UPDATE_ACCOUNT = "Updated account '%s' for '%s'";
    public static final String SUMMARY_DELETE_ACCOUNT = "Deleted account '%s' for '%s'";
    public static final String SUMMARY_CREATE_FUNCTION = "Created function id '%s', version '%s'";
    public static final String SUMMARY_UPDATE_FUNCTION = "Updated function id '%s',version '%s'";
    public static final String SUMMARY_DELETE_FUNCTION = "Deleted function id '%s',version '%s'";
    public static final String SUMMARY_ACTIVATE_FUNCTION =
            "Activated function id '%s', version '%s'";
    public static final String SUMMARY_ERROR_FUNCTION =
            "Errored function id '%s', version '%s'";
    public static final String SUMMARY_DEGRADING_FUNCTION =
            "Degrading function id '%s', version '%s'";
    public static final String SUMMARY_DEGRADED_FUNCTION =
            "Degraded function id '%s', version '%s'";
    public static final String SUMMARY_CREATE_FUNCTION_DEPLOYMENT =
            "Deployed function id '%s', version '%s'";
    public static final String SUMMARY_UPDATE_FUNCTION_DEPLOYMENT =
            "Updated deployment for function id '%s', version '%s'";
    public static final String SUMMARY_DELETE_FUNCTION_DEPLOYMENT =
            "Deleted deployment for function id '%s', version '%s'";
    public static final String SUMMARY_GRACEFUL_DELETE_FUNCTION_DEPLOYMENT =
            "Gracefully deleted deployment for function id '%s', version '%s'";
    public static final String SUMMARY_INACTIVATE_FUNCTION =
            "Inactivate function id '%s', version '%s'";
    public static final String SUMMARY_CREATE_REGISTRY_CREDENTIAL =
            "Created registry credential '%s' for account '%s'";
    public static final String SUMMARY_UPDATE_REGISTRY_CREDENTIAL =
            "Updated registry credential '%s' for account '%s'";
    public static final String SUMMARY_DELETE_REGISTRY_CREDENTIAL =
            "Deleted registry credential '%s' for account '%s'";

    public static final String STATE_CREATED = "CREATED";
    public static final String STATE_UPDATED = "UPDATED";
    public static final String STATE_DELETED = "DELETED";
    public static final String STATE_ACTIVATED = "ACTIVATED";
    public static final String STATE_ERROR = "ERROR";
    public static final String STATE_DEGRADED = "DEGRADED";
    public static final String STATE_DEGRADING = "DEGRADING";
    public static final String STATE_INACTIVE = "INACTIVE";

    public static final String FUNCTION_STATUS = "function_status";
    public static final String NCA_ID = "nca_id";
    public static final String ACCOUNT_NAME = "account_name";
    public static final String FUNCTION_ID = "function_id";
    public static final String FUNCTION_VERSION_ID = "function_version_id";
    public static final String DEPLOYMENT_ID = "deployment_id";
    public static final String REGISTRY_CREDENTIAL_ID = "registry_credential_id";
    public static final String REGISTRY_CREDENTIAL_HOSTNAME = "registry_credential_hostname";
    public static final String REGISTRY_CREDENTIAL_ARTIFACT_TYPES =
            "registry_credential_artifact_types";
    public static final String REGISTRY_CREDENTIAL_PROVISIONED_BY =
            "registry_credential_provisioned_by";

    public static final String TAG_FUNCTION_ID = "function_id";
    public static final String TAG_FUNCTION_VERSION_ID = "function_version_id";
    public static final String TAG_NCA_ID = "nca_id";
    public static final String TAG_INSTANCE_TYPE = "instance_type";

    public static final String ENC_KEY_NAME = "current-kid";

    public static final URI DEFAULT_HEALTH_ENDPOINT = URI.create("/v2/health/ready");
    public static final int DEFAULT_HEALTH_PORT = 8000;
    public static final Duration DEFAULT_HEALTH_TIMEOUT = Duration.ofSeconds(10);
    public static final int DEFAULT_HEALTH_EXPECTED_STATUS_CODE = 200;
    public static final Protocol DEFAULT_HEALTH_PROTOCOL = Protocol.HTTP;
    public static final String DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS =
            StringUtils.normalizeSpace("""
                                               tritonserver --model-repository /config/models
                                                            --model-control-mode none
                                                            --allow-metrics 1
                                                            --allow-gpu-metrics 1
                                                            --allow-cpu-metrics 1
                                                            --metrics-interval-ms 500
                                                            --strict-readiness 1
                                                            --log-error 1
                                                            --log-warning 1
                                                            --log-info 1
                                               """);

    public static final String SPAN_TAG_FUNCTION_ID = "function_id";
    public static final String SPAN_TAG_FUNCTION_VERSION_ID = "function_version_id";
    public static final String SPAN_TAG_NCA_ID = "nca_id";
    public static final String SPAN_TAG_INVOCATION_REQUEST_ID = "request_id";
    public static final String SPAN_TAG_INSTANCE_ID = "instance_id";
    public static final String SPAN_TAG_REGION = "region";
    public static final String SPAN_TAG_ISSUED_AT = "issued_at";
    public static final String SPAN_TAG_FUNCTION_STATUS = "function_status";
    public static final String SPAN_TAG_DEPLOYMENT_ID = "deployment_id";
    public static final String SPAN_TAG_INSTANCE_MANAGEMENT_TASK = "instance_management_task";

    // Could not use EnumSet as @RequestParam annotation expects a constant value for the
    // defaultValue property instead of an expression.
    public static final String DEFAULT_VISIBILITY = "authorized,private,public";

    public static final String ACCOUNT_NAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\-_]*$";

    // TODO lower to 5m when all python workers are gone and we can trust the heartbeat to extend the ttl
    // TODO ensure only the REQUEST TTL is lowered. we still need the streams to stay alive and
    // TODO the pre-signed urls to have an hour ttl.
    public static final Duration REQUEST_TTL = Duration.ofMinutes(60);

    public static final String TAG_REGEX = "[a-zA-Z0-9\\-_:=]+";

    public static final int MAX_SECRET_VALUE_LENGTH = 32768;
    public static final int MAX_SECRET_NAME_LENGTH = 48;

    // Max possible thread pool size for newWorkStealingPool
    public static final int MAX_THREAD_POOL_SIZE = 32;

    // must be a constant instead of config because we use it in annotations
    public static final int MAX_REQUEST_CONCURRENCY = 16384;

    // Hostname Syntax - https://en.wikipedia.org/wiki/Hostname#Syntax
    // public static final String HOSTNAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\-.]*$";
    // public static final String HOSTNAME_REGEX = "^[A-Za-z0-9][A-Za-z0-9-.]*\\.\\D{2,4}$";
    public static final String HOSTNAME_REGEX =
            "(?=^.{4,253}$)(^((?!-)[a-zA-Z0-9-]{1,63}(?<!-)\\.)+[a-zA-Z0-9]{2,63}$)";
    public static final int MAX_HOSTNAME_LENGTH = 255;

    // Could not use EnumSet as @RequestParam annotation expects a constant value for the
    // defaultValue property instead of an expression.
    // Lowercase values don't work in query parameter.
    public static final String DEFAULT_ARTIFACT_TYPE_ENUMS = "CONTAINER,HELM,MODEL,RESOURCE";
    public static final String DEFAULT_PROVISIONED_BY_ENUMS = "SYSTEM,USER";

    public static final int MAX_TAGS_COUNT = 64;
    public static final int MAX_TAG_LENGTH = 128;
    public static final int MAX_DESCRIPTION_LENGTH = 256;

    public static final UUID VERSION_WILDCARD = new UUID(0, 0);

    // Default values for per-account resources.
    public static final int DEFAULT_MAX_TELEMETRIES_ALLOWED = 25;
    public static final int DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED = 25;
    public static final int UPDATE_MAX_TELEMETRIES_ALLOWED = 50;
    public static final int UPDATE_MAX_REGISTRY_CREDENTIALS_ALLOWED = 50;
}
