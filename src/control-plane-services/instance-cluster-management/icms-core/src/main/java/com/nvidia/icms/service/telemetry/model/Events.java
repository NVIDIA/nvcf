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
package com.nvidia.icms.service.telemetry.model;

public enum Events {

    LATEST_INSTANCE_STATE("LatestInstanceState"),

    SHUTTING_DOWN_INSTANCES_WITHOUT_TERMINATED_STATE_UPDATE("ShuttingDownInstancesWithoutTerminatedStateUpdate"),

    SHUTTING_DOWN_INSTANCE_TERMINATION_TASK("ShuttingDownInstanceTerminationTask"),

    TERMINATED_STUCK_SHUTTING_DOWN_INSTANCE("TerminatedStuckShuttingDownInstance"),

    FAILED_TO_TERMINATE_STUCK_SHUTTING_DOWN_INSTANCE("FailedToTerminateStuckShuttingDownInstance"),

    ASYNC_EVENT_TRIGGER_FAILED("AsyncEventTriggerFailed"),

    NCA_ID_ACCOUNT_NAME_UPDATE_ASYNC_EVENT_FAILED("NcaIdAccountNameUpdateAsyncEventFailed"),

    NCA_ID_ACCOUNT_NAME_DETAILS("NcaIdAccountNameDetails"),

    NGC_INVOCATION_FAILED("NgcInvocationFailed"),

    EXPIRED_INSTANCE_TERMINATION_TASK("ExpiredInstanceTerminationTask"),

    CLUSTER_HEALTH_MONITOR_TASK("CloudHealthMonitorTask"),

    ACTIVE_INSTANCE_MONITORING_TASK("ActiveInstanceMonitoringTask"),

    ACTIVE_INSTANCE_MONITORING_TASK_FAILED("ActiveInstanceMonitoringTaskFailed"),

    GPU_USAGE_EVENT("GpuUsageEvent"),

    CANCEL_REQUEST_TASK("CancelRequestTask"),

    BART_HEARTBEAT_EVENT("BartHeartBeatEvent"),
    NVCA_HEARTBEAT_EVENT("NvcaHeartBeatEvent"),

    CREATE_INSTANCES("CreateInstances"),

    INSTANCE_TERMINATED_BY_USER("InstanceTerminatedByUser"),

    INSTANCE_TERMINATED_BY_ZONE("InstanceTerminatedByZone"),

    STARTED_PROCESSING_INSTANCE_REQUEST("StartedProcessingInstanceRequest"),

    STARTED_RUNNING_INSTANCE("StartedRunningInstance"),

    STARTING_INSTANCE("StartingInstance"),

    SHUTTING_DOWN_INSTANCE("ShuttingDownInstance"),

    CANCEL_INSTANCE_REQUEST("CancelInstanceRequest"),

    TERMINATE_INSTANCE_REQUEST("TerminateInstanceRequest"),

    API_RESPONSE_EVENT("ApiResponseEvent"),

    FILTER_PROCESSING_FAILED_EVENT("FilterProcessingFailed"),

    SCHEDULE_EXPIRED_STATE_UPDATE("ScheduleExpiredStateUpdate"),

    CANNOT_FULFILL_STATE_UPDATE("CannotFulfillStateUpdate"),

    ASYNC_TASK_CANCEL_INSTANCE_REQUEST("AsyncTaskCancelInstanceRequest"),

    INSTANCE_FAILED_CLOUD_OFFLINE("InstanceFailedCloudOffline"),

    TERMINATE_LIFETIME_EXPIRED_INSTANCE("TerminateLifetimeExpiredInstance"),

    CLOSE_LIFETIME_EXPIRED_INSTANCE_REQUEST("CloseLifetimeExpiredInstanceRequest"),

    BYOC_CLUSTER_REGISTERED("ByocClusterRegistered"),

    BYOC_CLUSTER_ABANDONED("ByocClusterAbandoned"),

    NVCA_CLUSTER_CREATED("NvcaClusterCreated"),

    ACTIVE_INSTANCE_FOR_REMOVED_INSTANCE_TYPES("ActiveInstancesForRemovedInstanceTypes"),

    NVCA_CLUSTER_RECONFIGURED("NvcaClusterReconfigured"),

    NVCA_CLUSTER_TERMINATED("NvcaClusterTerminated"),

    UNEXPECTED_ERROR_OCCURRED("UnexpectedErrorOccurred"),

    CLUSTER_MIGRATION_ASYNC_TASK_COMPLETED("ClusterMigrationAsyncTaskCompleted"),

    SNS_NOTIFICATION_FAILED("SnsNotificationFailed"),

    SQS_MESSAGING_FAILED("SqsMessagingFailed"),

    UNHEALTHY_CLOUD("UnhealthyCloud"),

    CACHE_BYTES_CONVERSION_FAILED("CacheBytesConversionFailed"),

    PRE_CONDITION_FAILED("PreConditionFailed"),

    CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID("ClusterInfoNotFoundForRequestId"),

    RECEIVED_SQS_BATCH_STATUS_UPDATE("ReceivedSqsBatchStatusUpdate"),

    RECEIVED_MULTIPLE_SQS_BATCH_STATUS_UPDATE("ReceivedMultipleSqsBatchStatusUpdate"),

    NVCA_CLUSTER_UPDATE("NvcaClusterUpdate"),

    INSTANCE_DELETION_FAILED("InstanceDeletionFailed"),

    REQUEST_DELETION_FAILED("RequestDeletionFailed"),

    STALE_REQUEST_DELETION_TASK("StaleRequestDeletionTask"),

    STATE_REQUEST_DELETION_EVENT("StaleRequestDeletionEvent"),

    REACTIVE_QUERY_FAILED("ReactiveQueryFailed"),

    STALE_DATA_DELETION("StaleDataDeletion"),

    UPDATE_CONFLICT_RESOLUTION("UpdateConflictResolution"),

    DB_INSERT_FAILED("DbInsertFailed"),

    DELETED_INCONSISTENT_DATA("DeletedInConsistentData"),

    INTERNAL_SERVER_ERROR_EVENT("InternalServerError"),

    DATABASE_CLEANUP_TASK("DatabaseCleanupTask"),

    GPUS_V5_POPULATION_EVENT("GpusV5PopulationTask"),

    ERROR_EVENT("ErrorEvent"),

    FUNCTION_DEPLOYMENT_STAGE("FunctionDeploymentStage"),

    FUNCTION_DEPLOYMENT_STAGE_V1("FunctionDeploymentStageV1"),

    SQS_QUEUE_ATTRIBUTE_UPDATE_FAILED("SqsQueueAttributeUpdateFailed"),

    GLOBAL_NATS_STREAM_VALIDATION_TASK("GlobalNatsStreamValidationTask"),

    WILD_CARD_NCA_ID_CACHE_TASK_NAME("WildCardNcaIdCacheInfoCacheTask"),

    WILD_CARD_NCA_ID_STALE_CACHE_DATA("WildCardNcaIdStaleCacheData"),

    GPU_USAGE_PER_INSTANCE("GpuUsagePerInstance"),

    RUNNING_DURATION_FINDING_FOR_GPU_USAGE_FAILED("RunningDurationFindingForGpuUsageFailed"),

    GPU_USAGE_PER_INSTANCE_EVENT_FAILED("GpuUsagePerInstanceEventFailed"),

    UNIFIED_ERROR("UnifiedError"),

    RESERVATION_DETAILS("ReservationDetails"),

    RESERVATION_ADDED("ReservationAdded"),

    RESERVATION_UPDATED("ReservationUpdated"),

    RESERVATION_DELETED("ReservationDeleted"),

    INSTANCE_CREATION_REQUEST_RECEIVED("InstanceCreationRequestReceived"),

    NVCA_VERSION_SELF_DESTRUCTION_VALIDATION_FAILED("NvcaVersionSelfDestructionValidationFailed"),

    NVCA_VERSION_SELF_DESTRUCTED("NvcaVersionSelfDestructed"),

    // Unhealthy instance processing task events
    PROCESS_UNHEALTHY_INSTANCE_TASK("ProcessUnhealthyInstanceTask"),

    PROCESS_UNHEALTHY_INSTANCE_TASK_FAILED("ProcessUnhealthyInstanceTaskFailed"),

    UNHEALTHY_INSTANCE_PROCESSING_FAILED("UnhealthyInstanceProcessingFailed"),

    // Reserved backup instance processing task events
    PROCESS_RESERVED_BACKUP_INSTANCE_TASK("ProcessReservedBackupInstanceTask"),

    PROCESS_RESERVED_BACKUP_INSTANCE_TASK_FAILED("ProcessReservedBackupInstanceTaskFailed"),

    BACKUP_TO_PRIMARY_ZONE_MIGRATION_SCHEDULED("BackupToPrimaryZoneMigrationScheduled"),

    EXPIRED_RESERVED_BACKUP_INSTANCE_TERMINATED("ExpiredReservedBackupInstanceTerminated"),

    REQUEST_STATE_TRANSITION_TO_ACTIVE("RequestStateTransitionToActive"),
    REQUEST_STATE_TRANSITION_TO_ACTIVE_FAILED("RequestStateTransitionToActiveFailed"),
    
    TENANT_REGISTRATION_CREATED("TenantRegistrationCreated"),

    TENANT_REGISTRATION_UPDATED("TenantRegistrationUpdated"),
    
    TENANT_REGISTRATION_DELETED("TenantRegistrationDeleted")

    ;


    private final String event;

    Events(String event) {
        this.event = event;
    }

    @Override
    public String toString() {
        return this.event;
    }
}
