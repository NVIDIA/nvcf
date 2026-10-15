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
package com.nvidia.icms.event;

import tools.jackson.core.JacksonException;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.ngc.NgcRequestHandler;
import com.nvidia.icms.outbound.ngc.model.AccountType;
import com.nvidia.icms.outbound.ngc.model.NgcNcaIdInfoResponse;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.List;

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NCA_ID_ACCOUNT_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class InstanceServiceEventListenerTest {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;
    
    @Mock
    private NgcRequestHandler ngcRequestHandler;
    
    @Mock
    private TelemetryEventClient telemetryEventClient;
    
    @Mock
    private InstanceServiceHelper instanceServiceHelper;
    
    @Captor
    private ArgumentCaptor<List<GenericMetric>> metricsCaptor;
    
    private InstanceServiceEventListener eventListener;
    
    @BeforeEach
    void setUp() {
        eventListener = new InstanceServiceEventListener(
                ngcRequestHandler,
                telemetryEventClient
        );
    }
    
    @Test
    void sendNcaIdAccountNameEventAsync_shouldUpdateAccountNameSuccessfully() throws JacksonException {
        // Arrange
        NcaIdAccountNameEvent event = NcaIdAccountNameEvent.builder()
                .ncaId(DUMMY_BYOC_NCA_ID)
                .requestId(DUMMY_REQUEST_ID)
                .build();
        
        when(ngcRequestHandler.getNcaIdDetails(DUMMY_BYOC_NCA_ID)).thenReturn(
                NgcNcaIdInfoResponse.builder()
                        .accountName(DUMMY_NCA_ID_ACCOUNT_NAME)
                        .build());
        
        // Act
        eventListener.sendNcaIdAccountNameEventAsync(event);
        
        // Assert
        verify(ngcRequestHandler).getNcaIdDetails(DUMMY_BYOC_NCA_ID);
        
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.NCA_ID_ACCOUNT_NAME_DETAILS.toString(), metric.getEventName());
        assertEquals(DUMMY_REQUEST_ID, metric.getRequestId());
        assertEquals(DUMMY_BYOC_NCA_ID, metric.getNcaId());
        assertEquals(DUMMY_NCA_ID_ACCOUNT_NAME, metric.getNcaIdPartnerName());
    }
    
    @Test
    void sendNcaIdAccountNameEventAsync_shouldHandleExceptionGracefully() throws JacksonException {
        // Arrange
        NcaIdAccountNameEvent event = NcaIdAccountNameEvent.builder()
                .ncaId(DUMMY_BYOC_NCA_ID)
                .requestId(DUMMY_REQUEST_ID)
                .build();
        
        String errorMessage = "Error fetching account name";
        RuntimeException testException = new RuntimeException(errorMessage);
        
        when(ngcRequestHandler.getNcaIdDetails(DUMMY_BYOC_NCA_ID)).thenThrow(testException);
        
        // Act
        eventListener.sendNcaIdAccountNameEventAsync(event);
        
        // Assert
        verify(ngcRequestHandler).getNcaIdDetails(DUMMY_BYOC_NCA_ID);
        verifyNoInteractions(instanceServiceHelper);
        verifyNoInteractions(instanceRequestV2Repository);
        
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.NCA_ID_ACCOUNT_NAME_UPDATE_ASYNC_EVENT_FAILED.toString(), metric.getEventName());
        assertEquals(DUMMY_REQUEST_ID, metric.getRequestId());
        assertEquals(DUMMY_BYOC_NCA_ID, metric.getNcaId());
        assertEquals(errorMessage, metric.getError());
    }
}
