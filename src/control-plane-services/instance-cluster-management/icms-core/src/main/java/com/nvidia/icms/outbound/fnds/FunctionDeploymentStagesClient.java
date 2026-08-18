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
package com.nvidia.icms.outbound.fnds;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.fnds.model.FndsMessageModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageV2Model;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.builder.CloudEventBuilder;
import io.cloudevents.core.format.EventFormat;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.http.ResponseEntity;
import org.springframework.stereotype.Component;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@RefreshScope
@Slf4j
@Component
public class FunctionDeploymentStagesClient {

    private static final String CLIENT_REGISTRATION_ID = "fnds";

    public static final String MESG_HTTP_ERROR_SENDING =
            "postMessage V3: Instance %s Error of sending function deployment stages data. " +
                    "HTTP code %s Message %s";

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;

    private final FndsStubService fndsStubService;
    private final EventFormat format;

    // Package-private constructor for unit testing with a mock FndsStubService
    FunctionDeploymentStagesClient(
            FndsStubService fndsStubService,
            IcmsConfigurationProperties icmsConfigurationProperties,
            TelemetryEventClient telemetryEventClient,
            EventFormat format) {
        this.fndsStubService = fndsStubService;
        this.icmsConfigurationProperties = icmsConfigurationProperties;
        this.telemetryEventClient = telemetryEventClient;
        this.format = format;
    }

    @Autowired
    public FunctionDeploymentStagesClient(
            @Value("${icms.fnds.fnds-base-url}") String fndsBaseUrl,
            @Value("${spring.security.oauth2.client.registration.fnds.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.fnds.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.fnds.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.fnds.token-uri}") String tokenUri,
            @NotNull IcmsConfigurationProperties icmsConfigurationProperties,
            @NotNull TelemetryEventClient telemetryEventClient,
            @NotNull EventFormat format,
            @Qualifier("fndsHttpResources") ManagedHttpResources httpResources,
            WebClient.Builder webClientBuilder) {  // Prototype-scoped - Safe to mutate.

        this.icmsConfigurationProperties = icmsConfigurationProperties;
        this.telemetryEventClient = telemetryEventClient;
        this.format = format;

        var webClient = webClientBuilder
                .baseUrl(fndsBaseUrl)
                .clientConnector(httpResources.connector())
                .filter(OAuth2ClientUtils.getOauth2ExchangeFilter(
                        CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope))
                .filter(OAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .build();

        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.fndsStubService = factory.createClient(FndsStubService.class);
    }

    public Integer sendFunctionDeploymentStage(@NotNull FndsMessageV2Model message) {
        if (!icmsConfigurationProperties.isFndsMessagesEnabled()) {
            log.debug("sendFunctionDeploymentStage: Instance {} Sending FnDS messages is disabled ",
                      message.getInstanceId());
            return null;
        }

        int responseHttpCode = 0;

        if (icmsConfigurationProperties.isFndsMessagesV1Enabled()) {
            responseHttpCode = postMessageV1(message);
        }

        if (icmsConfigurationProperties.isFndsMessagesV2Enabled()) {
            responseHttpCode = postMessageV2(message);
        }

        if (icmsConfigurationProperties.isFndsMessagesV3Enabled()) {
            responseHttpCode = postMessageV3(message);
        }

        return responseHttpCode;
    }

    private Integer postMessageV1(@NotNull FndsMessageV2Model message) {
        var body = toFndsMessageModel(message);
        log.debug("Sending FnDS V1: versionId={}, instanceId={}", message.getFunctionVersionId(),
                  message.getInstanceId());

        try {
            ResponseEntity<Void> response = fndsStubService.postDeploymentStageV1(
                    message.getFunctionVersionId(), message.getInstanceId(), body);

            int code = response.getStatusCode().value();
            String errorMessage = "";
            if (code != 202) {
                errorMessage = String.format(
                        "postMessage V1: Instance %s Error of sending function deployment data. HTTP code %s",
                        message.getInstanceId(), code);
                log.error(errorMessage);
            }
            sendStageTelemetryEvent(message, code, errorMessage, false);
            return code;
        } catch (WebClientResponseException ex) {
            int code = ex.getStatusCode().value();
            String errorMessage = String.format(
                    "postMessage V1: Instance %s Error of sending function deployment data. HTTP code %s",
                    message.getInstanceId(), code);
            log.error(errorMessage, ex);
            sendStageTelemetryEvent(message, code, errorMessage, false);
            return code;
        }
    }

    private Integer postMessageV2(@NotNull FndsMessageV2Model message) {
        log.debug("Sending FnDS V2: versionId={}, deploymentId={}, instanceId={}",
                  message.getFunctionVersionId(), message.getDeploymentId(),
                  message.getInstanceId());

        try {
            ResponseEntity<Void> response = fndsStubService.postDeploymentStageV2(
                    message.getFunctionVersionId(),
                    String.valueOf(message.getDeploymentId()),
                    message.getInstanceId(),
                    message);

            int code = response.getStatusCode().value();
            String errorMessage = "";
            if (code != 202) {
                errorMessage = String.format(
                        "postMessage V2: Instance %s Error of sending function deployment data. HTTP code %s",
                        message.getInstanceId(), code);
                log.error(errorMessage);
            }
            sendStageTelemetryEvent(message, code, errorMessage, true);
            return code;
        } catch (WebClientResponseException ex) {
            int code = ex.getStatusCode().value();
            String errorMessage = String.format(
                    "postMessage V2: Instance %s Error of sending function deployment data. HTTP code %s",
                    message.getInstanceId(), code);
            log.error(errorMessage, ex);
            sendStageTelemetryEvent(message, code, errorMessage, true);
            return code;
        }
    }

    private Integer postMessageV3(@NotNull FndsMessageV2Model message) {
        byte[] payload =
                ("[" + new String(format.serialize(buildCloudEvent(message))) + "]").getBytes();

        try {
            ResponseEntity<Void> response = fndsStubService.postCloudEvents(payload);

            int code = response.getStatusCode().value();
            String errorMessage = "";
            if (!Set.of(200, 202).contains(code)) {
                errorMessage =
                        String.format(MESG_HTTP_ERROR_SENDING, message.getInstanceId(), code, "");
                log.error(errorMessage);
            }
            sendStageTelemetryEvent(message, code, errorMessage);
            return code;
        } catch (WebClientResponseException ex) {
            int code = ex.getStatusCode().value();
            String errorMessage =
                    String.format(MESG_HTTP_ERROR_SENDING, message.getInstanceId(), code, ex.getMessage());
            log.error(errorMessage, ex);
            sendStageTelemetryEvent(message, code, errorMessage);
            return code;
        }
    }

    private CloudEvent buildCloudEvent(@NotNull FndsMessageV2Model message) {
        var type = FndsStages.fromText(message.getEvent()).getCloudEventType();
        CloudEventBuilder cloudEventBuilder = CloudEventBuilder.v1()
                .withId(UUID.randomUUID().toString())
                .withSource(URI.create("nvidia-spot"))
                .withTime(Instant.now().atOffset(ZoneOffset.UTC))
                .withType(type);
        if (Objects.nonNull(message.getFunctionVersionId())) {
            cloudEventBuilder.withExtension("namespace", message.getFunctionVersionId());
        }
        if (Objects.nonNull(message.getInstanceId())) {
            cloudEventBuilder.withExtension("instanceid", message.getInstanceId());
        }
        if (Objects.nonNull(message.getFunctionId())) {
            cloudEventBuilder.withExtension("functionid", message.getFunctionId());
        }
        if (Objects.nonNull(message.getFunctionVersionId())) {
            cloudEventBuilder.withExtension("functionversionid", message.getFunctionVersionId());
        }
        if (Objects.nonNull(message.getDeploymentId())) {
            cloudEventBuilder.withExtension("deploymentid", message.getDeploymentId().toString());
        }
        if (Objects.nonNull(message.getGpuSpecificationId())) {
            cloudEventBuilder.withExtension("gpuspecificationid", message.getGpuSpecificationId().toString());
        }
        if (Objects.nonNull(message.getNcaId())) {
            cloudEventBuilder.withExtension("ncaid", message.getNcaId());
        }
        cloudEventBuilder.withExtension("eventtype", "sis")
                .withData(GsonCompatMapper.toJson(message.getDetails())
                                  .getBytes(StandardCharsets.UTF_8));
        return cloudEventBuilder.build();
    }

    public void sendStageTelemetryEvent(@NotNull FndsMessageV2Model message, Integer httpCode, String errorMessage, boolean useV2Endpoint) {
        try {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TelemetryEventClient.EventMetaData.DEPLOYMENT_STAGE.getName(), message.getEvent());

            List<GenericMetric> genericMetricList = List.of(new GenericMetric()
                    .withEventName(String.valueOf(
                            useV2Endpoint ? Events.FUNCTION_DEPLOYMENT_STAGE : Events.FUNCTION_DEPLOYMENT_STAGE_V1))
                    .withError(errorMessage)
                    .withHttpCode(httpCode)
                    .withInstanceId(message.getInstanceId())
                    .withFunctionId(message.getFunctionId())
                    .withFunctionVersionId(message.getFunctionVersionId())
                    .withNcaId(message.getNcaId())
                    .withDeploymentId(message.getDeploymentId())
                    .withMetadata(metadata));

            telemetryEventClient.triggerEvent(genericMetricList);
        } catch (Exception ex) {
            log.error("Exception occurred while sending telemetry event for error handling {}", ex.getMessage());
        }
    }

    public void sendStageTelemetryEvent(@NotNull FndsMessageV2Model message, Integer httpCode, String errorMessage) {
        try {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TelemetryEventClient.EventMetaData.DEPLOYMENT_STAGE.getName(), message.getEvent());

            List<GenericMetric> genericMetricList = List.of(new GenericMetric()
                    .withEventName(String.valueOf(Events.FUNCTION_DEPLOYMENT_STAGE))
                    .withError(errorMessage)
                    .withHttpCode(httpCode)
                    .withInstanceId(message.getInstanceId())
                    .withFunctionId(message.getFunctionId())
                    .withFunctionVersionId(message.getFunctionVersionId())
                    .withNcaId(message.getNcaId())
                    .withDeploymentId(message.getDeploymentId())
                    .withGpuSpecificationId(message.getGpuSpecificationId())
                    .withMetadata(metadata));

            telemetryEventClient.triggerEvent(genericMetricList);
        } catch (Exception ex) {
            log.error("Exception occurred while sending telemetry event for error handling {}", ex.getMessage());
        }
    }

    private FndsMessageModel toFndsMessageModel(@NotNull FndsMessageV2Model fndsMessageV2Model) {
        return FndsMessageModel.builder()
                .instanceId(fndsMessageV2Model.getInstanceId())
                .functionId(fndsMessageV2Model.getFunctionId())
                .functionVersionId(fndsMessageV2Model.getFunctionVersionId())
                .ncaId(fndsMessageV2Model.getNcaId())
                .event(fndsMessageV2Model.getEvent())
                .eventType(fndsMessageV2Model.getEventType())
                .timestamp(fndsMessageV2Model.getTimestamp())
                .details(fndsMessageV2Model.getDetails())
                .build();
    }
}
