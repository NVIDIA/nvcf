// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

pub use stargate_auth::AuthTokenProvider;
pub use stargate_protocol::TunnelTransportProtocol;
pub use stargate_tls::ServerTlsIdentity;

mod bringup;
mod output_token_parser;
mod queue_admission;
mod quic_http_tunnel;
mod registration;
mod request_observer;
mod request_quality_monitor;
mod sse_message_stream;
mod stats;

pub use bringup::BringupConfig;
pub use output_token_parser::OutputTokenParserFactory;
pub use queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionTracker};
pub use quic_http_tunnel::{
    PylonRetryConfig, QuicHttpTunnelConfig, QuicHttpTunnelHandle, ReverseQuicTunnelConfig,
    ReverseQuicTunnelHandle, TunnelError, TunnelForwardingConfig, start_quic_http_tunnel,
    start_reverse_quic_tunnel,
};
pub use registration::{
    ClientError, CurrentModelStats, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, InferenceServerUpdateChannels,
};
pub use request_observer::{
    RequestObservation, RequestObservationEndpoint, RequestObservationState,
};
pub use request_quality_monitor::RequestQualityMonitorConfig;
pub use stats::{
    EngineStatsStreamConfig, EngineStatsStreamHandle, EngineStatsStreamMode, PylonMetrics,
    RequestCounterUpdate, RequestCounterUpdateInput, StatsAggregatorUpdate, StatsCollectorConfig,
    StatsCollectorHandle, StatsUpdateSource, parse_engine_stats_line_for_benchmark,
    request_observation_channel, start_engine_stats_stream, start_metrics_server,
    start_stats_collector, start_stats_collector_with_engine_stats,
    stats_aggregator_update_channel,
};
