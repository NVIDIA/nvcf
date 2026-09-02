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
package com.nvidia.icms.service;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForTerminateInstanceRequest;
import static java.util.stream.Collectors.toSet;

@Service
@Slf4j
@AllArgsConstructor
public class TerminateInstanceService {

    private final InstanceLifecycleService instanceLifecycleService;
    private final ByocService byocService;
    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final InstanceV2Repository instanceV2Repository;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final AppAuditService auditService;

    private final TelemetryEventClient telemetryEventClient;

    private final InstanceServiceHelper instanceServiceHelper;

    private final ComputePlatformService computePlatformService;

    @Observed
    public TerminateInstancesResponse terminateInstances(
            String customer,
            Set<String> instanceIds,
            Map<String, Object> auditProps) {
        Set<InstanceV2Entity> validatedInstances =
                validateInstanceTermination(customer, instanceIds);

        return terminateInstancesForAllProviders(validatedInstances, auditProps);
    }

    @Observed
    public TerminateInstancesResponse terminateInstances(
            @NotNull String ncaId,
            @Nullable UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            @NotNull String instanceId,
            @NotNull Map<String, Object> auditProps) {
        Set<InstanceV2Entity> validatedInstances =
                validateInstanceTermination(null, Set.of(instanceId));

        validatedInstances = validateInstancesOwnership(validatedInstances, ncaId, deploymentId, gpuSpecificationId);
        return terminateInstancesForAllProviders(validatedInstances, auditProps);
    }

    @Observed
    public TerminateInstancesResponse terminateInstances(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            int instanceCount,
            @NotNull Map<String, Object> auditProps) {
        if (instanceCount <= 0) {
            throw new IcmsBadRequestException("InstanceCount should be greater than zero");
        }

        List<InstanceV2Entity> deploymentInstances = gpuSpecificationId == null
                ? instanceV2Repository.findInstancesByDeploymentId(deploymentId)
                : instanceV2Repository.findInstancesByGpuSpecificationId(
                        deploymentId, gpuSpecificationId);
        Set<InstanceV2Entity> instances = selectInstancesToTerminate(
                deploymentInstances
                .stream()
                .filter(instance -> ncaId.equals(instance.getNcaId()))
                .filter(this::isInstanceRunning)
                .collect(toSet()),
                instanceCount);

        if (instances.isEmpty()) {
            return emptyResponse();
        }

        Set<String> instanceIds = instances.stream()
                .map(InstanceV2Entity::getInstanceId)
                .collect(toSet());
        Set<InstanceV2Entity> validatedInstances = validateInstanceTermination(null, instanceIds);
        validatedInstances = validateInstancesOwnership(
                validatedInstances, ncaId, deploymentId, gpuSpecificationId);
        return terminateInstancesForAllProviders(validatedInstances, auditProps);
    }

    // Keep this selection order in sync with NVCF's IcmsAllocatorService.
    private static Set<InstanceV2Entity> selectInstancesToTerminate(
            Set<InstanceV2Entity> instances,
            int count) {
        return instances.stream()
                .filter(instance -> instance.getInstanceId() != null)
                .sorted(Comparator
                        .comparing((InstanceV2Entity instance) ->
                                instance.getInstanceStateName() == SpotInstanceInternalState.RUNNING)
                        .thenComparing(instance ->
                                TimeUtils.getInstantFromUuid(instance.getCreateTimeuuid())))
                .limit(count)
                .collect(toSet());
    }


    @Observed
    public TerminateInstancesResponse terminateInstanceRequests(
            String customer,
            Set<String> requestIds,
            Map<String, Object> auditProps) {

        Set<InstanceRequestV2Entity> validatedRequests =
                validateInstanceRequestTermination(customer, requestIds, null);

        return terminateRequestsForAllProviders(validatedRequests, auditProps);
    }

    @Observed
    public TerminateInstancesResponse terminateInstanceRequests(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            @NotNull String requestId,
            @Nullable InstanceRequestV2Entity cachedRequest,
            Map<String, Object> auditProps) {

        InstanceRequestV2Entity resolved = cachedRequest != null ? cachedRequest
                : instanceRequestV2Repository.findRequestById(requestId).orElse(null);
        InstanceRequestV2Entity request = validateRequestOwnership(resolved, ncaId, deploymentId, gpuSpecificationId, "request Id", requestId);

        if (request == null) {
            return emptyResponse();
        }

        Set<InstanceRequestV2Entity> validatedRequests =
                validateInstanceRequestTermination(null, Set.of(requestId), Set.of(request));

        return terminateRequestsForAllProviders(validatedRequests, auditProps);
    }


    @Observed
    public TerminateInstancesResponse instanceDeploymentTermination(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            Map<String, Object> auditProps) {

        TerminateInstancesResponse response = emptyResponse();

        List<InstanceRequestV2Entity> requests = instanceRequestV2Repository.findRequestsByDeploymentId(ncaId, deploymentId, null);
        if (gpuSpecificationId != null) {
            requests = requests.stream().filter(r -> gpuSpecificationId.equals(r.getGpuSpecificationId())).toList();
        }

        if (requests.isEmpty()) {
            return response;
        }

        requests.forEach(request ->
                response.getTerminatingInstances().addAll(
                        terminateInstanceRequests(ncaId, deploymentId, gpuSpecificationId,
                                               request.getRequestId(), request, auditProps)
                                .getTerminatingInstances()));

        return response;
    }


    private TerminateInstancesResponse terminateInstancesForAllProviders(
            @NotNull Set<InstanceV2Entity> instances,
            @NotNull Map<String, Object> auditProps) {

        TerminateInstancesResponse terminateInstancesResponse =
                emptyResponse();

        terminateInstancesForProvider(ResourceProvider.BYOC, instances, auditProps, terminateInstancesResponse);
        for (ResourceProvider provider : computePlatformService.computePlatformResourceProviders()) {
            terminateInstancesForProvider(provider, instances, auditProps, terminateInstancesResponse);
        }

        return terminateInstancesResponse;
    }

    private TerminateInstancesResponse terminateRequestsForAllProviders(
            @NotNull Set<InstanceRequestV2Entity> requests,
            @NotNull Map<String, Object> auditProps) {

        TerminateInstancesResponse terminateInstancesResponse =
                emptyResponse();

        for (ResourceProvider provider : computePlatformService.computePlatformResourceProviders()) {
            terminateInstancesInRequestsForProvider(provider, requests, auditProps, terminateInstancesResponse);
        }
        terminateInstancesInRequestsForProvider(ResourceProvider.BYOC, requests, auditProps, terminateInstancesResponse);

        return terminateInstancesResponse;
    }

    private void terminateInstancesInRequestsForProvider(@NotNull ResourceProvider provider,
                                                         @NotNull Set<InstanceRequestV2Entity> requests,
                                                         @NotNull Map<String, Object> auditProps,
                                                         @NotNull TerminateInstancesResponse response) {
        Set<InstanceRequestV2Entity> providerRequests = requests.stream()
                .filter(r -> provider == r.getResourceProvider())
                .collect(Collectors.toSet());

        if (providerRequests.isEmpty()) {
            return;
        }

        InstanceRequestV2Entity firstRequest = providerRequests.iterator().next();

        // Collecting running instances
        Map<String, InstanceV2Entity> runningInstanceMap =
                getRunningInstancesForRequestIds(null, providerRequests);

        // Terminating running instances using provider-specific service
        if (!runningInstanceMap.isEmpty()) {
            TerminateInstancesResponse terminated;
            if (computePlatformService.isComputePlatformProvider(provider)) {
                terminated = instanceLifecycleService.terminateInstanceRequests(
                        firstRequest.getCustomer(), runningInstanceMap, auditProps);
            } else if (provider == ResourceProvider.BYOC) {
                terminated = byocService.terminateInstanceRequests(runningInstanceMap, auditProps);
            } else {
                throw new IcmsInternalServerException(
                        String.format("%s resource provider is not supported for termination", provider));
            }
            response.getTerminatingInstances().addAll(terminated.getTerminatingInstances());
        }

        // Closing the requests
        updateRequestStateToClosed(firstRequest.getCustomer(), providerRequests, provider, auditProps);
    }


    private void terminateInstancesForProvider(@NotNull ResourceProvider provider,
                                    @NotNull Set<InstanceV2Entity> instances,
                                    @NotNull Map<String, Object> auditProps,
                                    @NotNull TerminateInstancesResponse response) {
        Set<InstanceV2Entity> providerInstances = instances.stream()
                .filter(i -> provider == i.getResourceProvider())
                .collect(Collectors.toSet());

        if (providerInstances.isEmpty()) {
            return;
        }

        TerminateInstancesResponse terminated;
        if (computePlatformService.isComputePlatformProvider(provider)) {
            terminated = instanceLifecycleService.terminateInstances(
                    providerInstances.iterator().next().getCustomer(), providerInstances, auditProps);
        } else if (provider == ResourceProvider.BYOC) {
            terminated = byocService.terminateInstances(providerInstances, auditProps);
        } else {
            throw new IcmsInternalServerException(
                    String.format("%s resource provider is not supported for termination", provider));
        }

        response.getTerminatingInstances().addAll(terminated.getTerminatingInstances());
    }



    private Set<InstanceV2Entity> validateInstancesOwnership(@NotNull Set<InstanceV2Entity> instances,
                                                                 @NotNull String ncaId,
                                                                 @Nullable UUID deploymentId,
                                                                 @Nullable UUID gpuSpecificationId) {
        Set<InstanceV2Entity> result = new HashSet<>();

        Map<String, InstanceRequestV2Entity> requestCache = new HashMap<>();

        for (InstanceV2Entity instance : instances) {
            InstanceRequestV2Entity request = requestCache.computeIfAbsent(
                    instance.getRequestId(),
                    id -> instanceRequestV2Repository.findRequestById(id).orElse(null));

            request = validateRequestOwnership(request,
                                               ncaId,
                                               deploymentId,
                                               gpuSpecificationId,
                                               "instance Id",
                                               instance.getInstanceId());
            if (request != null) {
                result.add(instance);
            }
        }

        return result;
    }


    private InstanceRequestV2Entity validateRequestOwnership(@Nullable InstanceRequestV2Entity request,
                                                         @NotNull String ncaId,
                                                         @Nullable UUID deploymentId,
                                                         @Nullable UUID gpuSpecificationId,
                                                         @NotNull String logIdName,
                                                         @NotNull String logId) {
        if (request == null) {
            return null;
        }
        if (!ncaId.equals(request.getNcaId())) {
            log.warn("Termination ownership validation: {} {} does not belong to ncaId {}",
                     logIdName, logId, ncaId);
            return null;
        }
        if (deploymentId != null && !deploymentId.equals(request.getDeploymentId())) {
            log.warn("Termination ownership validation: {} {} does not belong to deploymentId {}",
                     logIdName, logId, deploymentId);
            return null;
        }
        if (gpuSpecificationId != null && !gpuSpecificationId.equals(request.getGpuSpecificationId())) {
            log.warn("Termination ownership validation: {} {} does not belong to gpu spec id {}",
                     logIdName, logId, gpuSpecificationId);
            return null;
        }
        return request;
    }



    // Instance termination helper functions
    private Set<InstanceV2Entity> validateInstanceTermination(
            String customer,
            Set<String> instanceIds) {

        List<InstanceV2Entity> instanceEntities =
                instanceV2Repository.findInstancesByCustomerAndIds(customer, instanceIds);
        if (instanceEntities.isEmpty()) {
            throw new IcmsNotFoundException(String.format("Invalid instance ids - %s", instanceIds));
        }

        Set<String> instancesReceivedFromUser = new HashSet<>(instanceIds);
        Set<String> instancesExistedInDb = new HashSet<>();
        Set<InstanceV2Entity> validatedInstances = new HashSet<>();

        for (InstanceV2Entity instanceEntity : instanceEntities) {
            instancesExistedInDb.add(instanceEntity.getInstanceId());

            ResourceProvider resourceProvider = instanceEntity.getResourceProvider();
            if (resourceProvider == null) {
                log.warn("Skipping instance {} — resourceProvider is null", instanceEntity.getInstanceId());
                continue;
            }
            // Non-BYOC or BYOC providers (i.e. not legacy OCI) are valid for termination.
            if (computePlatformService.isComputePlatformProvider(resourceProvider) || resourceProvider == ResourceProvider.BYOC) {
                validatedInstances.add(instanceEntity);
            } else {
                String errMsg = String.format("%s resource provider mapping doesn't exist",
                                              resourceProvider);
                log.error("error: {}", errMsg);
                throw new IcmsInternalServerException(errMsg);
            }
        }

        checkForUnknownIds(instancesReceivedFromUser, instancesExistedInDb, "instance ids");

        return validatedInstances;
    }


    private TerminateInstancesResponse emptyResponse() {
        return new TerminateInstancesResponse(new ArrayList<>());
    }

    private void checkForUnknownIds(
            @NotNull Set<String> requestedIds,
            @NotNull Set<String> foundIds,
            @NotNull String label) {
        Set<String> unknown = new HashSet<>(requestedIds);
        unknown.removeAll(foundIds);
        if (!unknown.isEmpty()) {
            log.error("Invalid {} provided {}", label, unknown);
            throw new IcmsNotFoundException(String.format("Invalid %s - %s", label, unknown));
        }
    }

    private List<InstanceRequestV2Entity> resolveInstanceRequestEntities(
            @NotNull Set<String> requestIds,
            @Nullable String customer,
            @Nullable Set<InstanceRequestV2Entity> cachedRequests) {

        Map<String, InstanceRequestV2Entity> cacheMap = cachedRequests == null ? Map.of()
                : cachedRequests.stream()
                        .collect(Collectors.toMap(InstanceRequestV2Entity::getRequestId, r -> r, (a, b) -> a));

        List<InstanceRequestV2Entity> resolved = new ArrayList<>();
        for (String requestId : requestIds) {
            InstanceRequestV2Entity request = cacheMap.containsKey(requestId)
                    ? cacheMap.get(requestId)
                    : instanceRequestV2Repository.findRequestByIdAndCustomer(requestId, customer).orElse(null);
            if (request != null) {
                resolved.add(request);
            }
        }
        return resolved;
    }

    // Instance request termination helper functions
    private Set<InstanceRequestV2Entity> validateInstanceRequestTermination(
            @Nullable String customer,
            @NotNull Set<String> requestIds,
            @Nullable Set<InstanceRequestV2Entity> cachedRequests) {

        List<InstanceRequestV2Entity> instanceRequestEntityList =
                resolveInstanceRequestEntities(requestIds, customer, cachedRequests);

        if (instanceRequestEntityList.isEmpty()) {
            throw new IcmsNotFoundException(String.format("Invalid request ids - %s", requestIds));
        }

        Set<String> requestExistedInDb = new HashSet<>();
        Set<String> requestReceivedFromUser = new HashSet<>(requestIds);
        Set<InstanceRequestV2Entity> validatedRequests = new HashSet<>();

        for (InstanceRequestV2Entity instanceRequestEntity : instanceRequestEntityList) {
            String requestId = instanceRequestEntity.getRequestId();
            requestExistedInDb.add(requestId);

            // Allow termination of both OPEN and ACTIVE requests
            if (InstanceServiceHelper
                    .isRequestInOpenOrActiveState(instanceRequestEntity, icmsConfigurationProperties)) {
                // Resource Provider for non BYOC cluster Targeted will be BYOC
                // Non BYOC resourceProvider is used only in current compute platform provider backend flow
                // where compute platform provider is registered as a BART cluster
                ResourceProvider resourceProvider = instanceRequestEntity.getResourceProvider();
                if (resourceProvider == null) {
                    log.warn("Skipping request {} — resourceProvider is null", instanceRequestEntity.getRequestId());
                    continue;
                }
                // Non-BYOC or BYOC providers (i.e. not legacy OCI) are valid for termination.
                if (computePlatformService.isComputePlatformProvider(resourceProvider) || resourceProvider == ResourceProvider.BYOC) {
                    validatedRequests.add(instanceRequestEntity);
                } else {
                    String errorMsg = String.format("%s resource provider mapping doesn't exist",
                                                    resourceProvider);
                    log.error("error: {}", errorMsg);
                    throw new IcmsInternalServerException(errorMsg);
                }
            }
        }

        checkForUnknownIds(requestReceivedFromUser, requestExistedInDb, "requestIds provided");

        return validatedRequests;
    }

    private void updateRequestStateToClosed(
            String customer,
            Set<InstanceRequestV2Entity> instanceRequestEntitySet,
            ResourceProvider resourceProvider,
            Map<String, Object> auditProps) {

        if (instanceRequestEntitySet.isEmpty()) {
            return;
        }

        List<InstanceRequestV2Entity> entitiesBefore = new ArrayList<>();
        List<InstanceRequestV2Entity> entitiesAfter = new ArrayList<>();
        List<String> requestIdsForTermination = new ArrayList<>();

        for (InstanceRequestV2Entity entity : instanceRequestEntitySet) {
            entitiesBefore.add(AuditUtils.deepCopyInstanceRequestEntity(entity));
            entity.setAction(SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST);
            entity.setState(SpotInstanceRequestState.CLOSED);
            entity.setStatusCode(SpotRequestStatusCode.REQUEST_TERMINATED_BY_USER.toString());
            entity.setStatusMessage("Your instance request is closed");
            entity.setStatusUpdateTime(Instant.now());
            entitiesAfter.add(AuditUtils.deepCopyInstanceRequestEntity(entity));
            requestIdsForTermination.add(entity.getRequestId());
        }
        try {
            instanceRequestV2Repository.updateRequests(List.copyOf(instanceRequestEntitySet));
        } catch (Exception e) {
            log.error("failed to update state of requestIds {} in DB when closing.",
                      requestIdsForTermination, e);
            // Rethrowing same exception as it is handled in global error handler
            throw e;
        }

        sendAuditEvents(auditProps, entitiesBefore, entitiesAfter);
        // Sending event for terminating an instance request.
        sendRequestStateChangeEvent(instanceRequestEntitySet, customer, resourceProvider);
    }

    public void updateRequestStateToClosedFromAsyncTerminateTask(
            InstanceRequestV2Entity instanceRequestEntity) {
        if (instanceRequestEntity == null) {
            return;
        }

        instanceRequestEntity.setState(SpotInstanceRequestState.CLOSED);
        instanceRequestEntity.setStatusCode(
                SpotRequestStatusCode.INSTANCE_TERMINATED_BY_SERVICE.toString());
        instanceRequestEntity.setStatusMessage(
                "Your instance request is closed as lifetime of request has been expired");
        instanceRequestEntity.setStatusUpdateTime(Instant.now());

        try {
            instanceRequestV2Repository.updateRequests(List.of(instanceRequestEntity));
        } catch (Exception e) {
            log.error("failed to update state of requestId {} in DB when closing.",
                      instanceRequestEntity.getRequestId(), e);
            // Rethrowing same exception as it is handled in global error handler
            throw e;
        }

        // Sending event for terminating an instance request.
        sendRequestStateChangeEventForAsyncTerminateTask(instanceRequestEntity);
    }

    private void sendAuditEvents(
            @NotNull Map<String, Object> auditProps,
            @NotNull List<InstanceRequestV2Entity> entitiesBefore,
            @NotNull List<InstanceRequestV2Entity> entitiesAfter) {
        if (entitiesBefore.size() != entitiesAfter.size()) {
            log.warn("sendAuditEvents: entitiesBefore and entitiesAfter sizes differ"
                             + " (before={}, after={}); audit events will only be sent for the first {} record(s)",
                     entitiesBefore.size(), entitiesAfter.size(),
                     Math.min(entitiesBefore.size(), entitiesAfter.size()));
        }
        int limit = Math.min(entitiesBefore.size(), entitiesAfter.size());
        for (int i = 0; i < limit; i++) {
            populateAuditValuesForTerminateInstanceRequest(auditProps, entitiesBefore.get(i).getRequestId());
            auditService.sendAuditEventForInstanceRequest(auditProps, entitiesBefore.get(i), entitiesAfter.get(i));
        }
    }

    private void sendRequestStateChangeEventForAsyncTerminateTask(
            InstanceRequestV2Entity entity) {
        telemetryEventClient.triggerEvent(List.of(
                buildRequestMetric(entity, Events.CLOSE_LIFETIME_EXPIRED_INSTANCE_REQUEST.toString(),
                                   entity.getCustomer(), entity.getResourceProvider())));
    }

    private void sendRequestStateChangeEvent(
            Set<InstanceRequestV2Entity> entities, String customer,
            ResourceProvider resourceProvider) {
        List<GenericMetric> genericMetricList = entities.stream()
                .map(e -> buildRequestMetric(e, Events.TERMINATE_INSTANCE_REQUEST.toString(),
                                             customer, resourceProvider))
                .toList();
        telemetryEventClient.triggerEvent(genericMetricList);
    }

    private GenericMetric buildRequestMetric(
            @NotNull InstanceRequestV2Entity entity,
            @NotNull String eventName,
            String customer,
            ResourceProvider resourceProvider) {
        ClientRequestDataModel.LaunchSpecification ls =
                instanceServiceHelper.getLaunchSpecificationForTelemetry(entity.getRequest());
        return new GenericMetric()
                .withEventName(eventName)
                .withCustomer(customer)
                .withResourceProvider(resourceProvider)
                .withRequestId(entity.getRequestId())
                .withInstanceType(ls.getInstanceType())
                .withFunctionId(ls.getFunctionId())
                .withFunctionVersionId(ls.getVersionId())
                .withNcaId(ls.getNcaId())
                .withNcaIdPartnerName(ls.getNcaIdAccountName())
                .withRequestState(entity.getState().toString())
                .withDeploymentId(entity.getDeploymentId())
                .withGpuSpecificationId(entity.getGpuSpecificationId());
    }

    private Map<String, InstanceV2Entity> getRunningInstancesForRequestIds(
            @Nullable String customer,
            @NotNull Set<InstanceRequestV2Entity> instanceRequestEntitySet) {

        Set<String> requestIds = getRequestIdsWithInstances(instanceRequestEntitySet);
        return instanceV2Repository.findInstancesByCustomerAndRequestIds(customer, requestIds)
                .stream()
                .filter(this::isInstanceRunning)
                .collect(Collectors.toMap(InstanceV2Entity::getInstanceId, i -> i));
    }

    // Accept both PENDING_FULFILLMENT and FULFILLED status codes to support the
    // request-state-transition feature:
    // PENDING_FULFILLMENT: OPEN state requests with instances
    // FULFILLED: ACTIVE state requests (when feature flag is enabled)
    private Set<String> getRequestIdsWithInstances(
            @NotNull Set<InstanceRequestV2Entity> instanceRequestEntitySet) {
        return instanceRequestEntitySet.stream()
                .filter(r -> PENDING_FULFILLMENT.toString().equals(r.getStatusCode())
                          || SpotRequestStatusCode.FULFILLED.toString().equals(r.getStatusCode()))
                .map(InstanceRequestV2Entity::getRequestId)
                .collect(Collectors.toSet());
    }

    // Only request-state: "active" instances (internal-state: "starting" or "running")
    // will be considered for termination
    private boolean isInstanceRunning(@NotNull InstanceV2Entity instance) {
        return SpotInstanceRequestState.ACTIVE == instance.getRequestState()
            && (instance.getInstanceStateName() == SpotInstanceInternalState.RUNNING
             || instance.getInstanceStateName() == SpotInstanceInternalState.STARTING);
    }

}
