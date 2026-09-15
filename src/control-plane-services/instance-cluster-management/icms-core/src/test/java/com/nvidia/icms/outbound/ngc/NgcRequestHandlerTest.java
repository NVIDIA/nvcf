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
package com.nvidia.icms.outbound.ngc;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.ngc.NgcConfigurationProperties;
import com.nvidia.icms.outbound.ngc.model.AccountType;
import com.nvidia.icms.outbound.ngc.model.GetOrganizationResponse;
import com.nvidia.icms.outbound.ngc.model.GetOrganizationResponse.Organization;
import com.nvidia.icms.outbound.ngc.model.NgcNcaIdInfoResponse;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.io.IOException;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.reactive.function.client.WebClientResponseException;

@ExtendWith(MockitoExtension.class)
class NgcRequestHandlerTest {

    private static final String TEST_NCA_ID = "test-nca-id";
    private static final String TEST_DISPLAY_NAME = "Test Organization";
    private static final String DUMMY_NV_IDP_ID = "hg_3K";

    @Mock
    private NgcClient ngcClient;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private NgcConfigurationProperties ngcConfigurationProperties;

    @Captor
    private ArgumentCaptor<List<GenericMetric>> metricsCaptor;

    private NgcRequestHandler ngcRequestHandler;

    @BeforeEach
    void setUp() {
        ngcRequestHandler = new NgcRequestHandler(ngcClient, telemetryEventClient, ngcConfigurationProperties);
    }

    @Test
    void getNcaIdDetails_success() {
        // Arrange
        GetOrganizationResponse orgResponse = GetOrganizationResponse.builder()
                .organization(Organization.builder()
                                      .displayName(TEST_DISPLAY_NAME)
                                      .idpId(DUMMY_NV_IDP_ID)
                                      .build())
                .build();

        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenReturn(orgResponse);
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);
        when(ngcConfigurationProperties.getNvidiaInternalIdpId()).thenReturn(DUMMY_NV_IDP_ID);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertEquals(TEST_DISPLAY_NAME, result.getAccountName());
        assertEquals(AccountType.INTERNAL, result.getAccountType());
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void getNcaIdDetails_nullBody() {
        // Arrange
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenReturn(null);
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNull(result);
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(Events.NGC_INVOCATION_FAILED.toString(), metrics.get(0).getEventName());
        assertEquals("Failed to extract NCA ID information, responseBody is null", metrics.get(0).getError());
    }

    @Test
    void getNcaIdDetails_nullOrganization() {
        // Arrange
        GetOrganizationResponse orgResponse = GetOrganizationResponse.builder().build();
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenReturn(orgResponse);
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNull(result);
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(Events.NGC_INVOCATION_FAILED.toString(), metrics.get(0).getEventName());
        assertEquals("Failed to extract NCA ID information, getOrganization is null", metrics.get(0).getError());
    }

    @Test
    void getNcaIdDetails_accountNameNull() {
        // Arrange
        GetOrganizationResponse orgResponse = GetOrganizationResponse.builder()
                .organization(Organization.builder()
                                      .idpId(DUMMY_NV_IDP_ID)
                                      .build())
                .build();
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenReturn(orgResponse);
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);
        when(ngcConfigurationProperties.getNvidiaInternalIdpId()).thenReturn(DUMMY_NV_IDP_ID);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNotNull(result);
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(Events.NGC_INVOCATION_FAILED.toString(), metrics.getLast().getEventName());
        assertEquals("displayName is empty in response", metrics.getLast().getError());
    }

    @Test
    void getNcaIdDetails_idpIdNull_externalOrg() {
        // Arrange
        GetOrganizationResponse orgResponse = GetOrganizationResponse.builder()
                .organization(Organization.builder()
                                      .displayName(TEST_DISPLAY_NAME)
                                      .build())
                .build();
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenReturn(orgResponse);
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNotNull(result);
        assertEquals(TEST_DISPLAY_NAME, result.getAccountName());
        assertEquals(AccountType.EXTERNAL, result.getAccountType());
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void getNcaIdDetails_webClientResponseException() {
        // Arrange
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenThrow(
                WebClientResponseException.create(404, "Not Found", null, null, null));
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNull(result);
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(Events.NGC_INVOCATION_FAILED.toString(), metrics.get(0).getEventName());
        assertTrue(metrics.get(0).getError().contains("Failed to invoke NGC service for orgInfo"));
    }

    @Test
    void getNcaIdDetails_generalException() {
        // Arrange
        when(ngcClient.getOrgInfo(TEST_NCA_ID)).thenThrow(new RuntimeException("Failed to refresh token"));
        when(ngcConfigurationProperties.isEnabled()).thenReturn(true);

        // Act
        NgcNcaIdInfoResponse result = ngcRequestHandler.getNcaIdDetails(TEST_NCA_ID);

        // Assert
        assertNull(result);
        verify(ngcClient).getOrgInfo(TEST_NCA_ID);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(Events.NGC_INVOCATION_FAILED.toString(), metrics.get(0).getEventName());
        assertTrue(metrics.get(0).getError().contains("Failed to invoke NGC service for orgInfo"));
    }
}
