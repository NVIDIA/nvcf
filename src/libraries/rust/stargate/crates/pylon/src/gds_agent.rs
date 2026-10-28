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

//! Registers Pylon as the PYLON agent of its pool in the Global Discovery
//! Service (GDS) when `--gds-pool-name` is set. Registration retries in the
//! background until GDS accepts it, so GDS never delays startup. The
//! `models` attribute follows the served model list, and the agent is
//! deregistered after the tunnel drain on shutdown; the agent lease covers a
//! deregistration that fails or never runs.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;
use std::time::Duration;

use gds_client::v1::FrontendRole;
use gds_client::{AgentRegistration, AgentSpec, GdsClient};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// The agent attribute listing the served model ids, comma separated.
pub(crate) const MODELS_ATTRIBUTE: &str = "models";

/// How often the served model list is compared with the registered one.
const MODEL_POLL_INTERVAL: Duration = Duration::from_secs(5);

/// How long shutdown waits for `DeregisterPoolAgent`.
const DEREGISTER_TIMEOUT: Duration = Duration::from_secs(5);

/// Returns the model ids Pylon serves.
pub(crate) type ModelIds = Arc<dyn Fn() -> Vec<String> + Send + Sync>;

/// The PYLON agent registration of one pylon.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct GdsAgentConfig {
    pub(crate) clusterset: String,
    /// `--cluster-id`.
    pub(crate) cluster: String,
    /// `--gds-pool-name`.
    pub(crate) pool_name: String,
    /// `--inference-server-id`.
    pub(crate) agent_id: String,
    /// The requested agent lease; GDS's default (60 s) when unset.
    pub(crate) lease_ttl: Option<Duration>,
    pub(crate) model_poll_interval: Duration,
}

impl GdsAgentConfig {
    pub(crate) fn new(
        clusterset: impl Into<String>,
        cluster: impl Into<String>,
        pool_name: impl Into<String>,
        agent_id: impl Into<String>,
    ) -> Self {
        Self {
            clusterset: clusterset.into(),
            cluster: cluster.into(),
            pool_name: pool_name.into(),
            agent_id: agent_id.into(),
            lease_ttl: None,
            model_poll_interval: MODEL_POLL_INTERVAL,
        }
    }

    fn spec(&self, model_ids: &[String]) -> AgentSpec {
        let mut spec = AgentSpec::new(
            self.clusterset.clone(),
            self.cluster.clone(),
            self.pool_name.clone(),
            FrontendRole::Pylon,
            self.agent_id.clone(),
        );
        spec.attributes = model_attributes(model_ids);
        spec.lease_ttl = self.lease_ttl;
        spec
    }
}

/// `{"models": "<id>,<id>"}` with the ids sorted and deduplicated, or no
/// attribute while Pylon serves no model.
pub(crate) fn model_attributes(model_ids: &[String]) -> BTreeMap<String, String> {
    let model_ids: BTreeSet<&str> = model_ids
        .iter()
        .map(|model_id| model_id.trim())
        .filter(|model_id| !model_id.is_empty())
        .collect();
    if model_ids.is_empty() {
        return BTreeMap::new();
    }
    BTreeMap::from([(
        MODELS_ATTRIBUTE.to_string(),
        model_ids.into_iter().collect::<Vec<_>>().join(","),
    )])
}

/// The background registration. Dropping it aborts the task without
/// deregistering; the agent lease then expires.
pub(crate) struct GdsAgent {
    config: GdsAgentConfig,
    stop: CancellationToken,
    task: Option<JoinHandle<Option<AgentRegistration>>>,
}

impl GdsAgent {
    /// Starts registering on the current Tokio runtime and returns at once.
    pub(crate) fn start(client: GdsClient, config: GdsAgentConfig, model_ids: ModelIds) -> Self {
        info!(
            clusterset = %config.clusterset,
            cluster_id = %config.cluster,
            pool = %config.pool_name,
            agent_id = %config.agent_id,
            "registering with GDS as the pool's PYLON agent"
        );
        let stop = CancellationToken::new();
        let task = tokio::spawn(run(client, config.clone(), model_ids, stop.clone()));
        Self {
            config,
            stop,
            task: Some(task),
        }
    }

    /// Stops following the models and deregisters the agent. Call it after
    /// the tunnel drain, so routers keep the pool while requests complete.
    pub(crate) async fn shutdown(mut self) {
        self.stop.cancel();
        let Some(task) = self.task.take() else {
            return;
        };
        let registration = match task.await {
            Ok(registration) => registration,
            Err(error) => {
                warn!(
                    pool = %self.config.pool_name,
                    cluster_id = %self.config.cluster,
                    %error,
                    "GDS pool agent task failed; the agent lease will expire"
                );
                return;
            }
        };
        let Some(registration) = registration else {
            info!(
                pool = %self.config.pool_name,
                cluster_id = %self.config.cluster,
                "GDS pool agent was never registered; nothing to deregister"
            );
            return;
        };
        match tokio::time::timeout(DEREGISTER_TIMEOUT, registration.shutdown()).await {
            Ok(Ok(())) => {}
            Ok(Err(error)) => warn!(
                pool = %self.config.pool_name,
                cluster_id = %self.config.cluster,
                %error,
                "GDS pool agent deregistration failed; the agent lease will expire"
            ),
            Err(_) => warn!(
                pool = %self.config.pool_name,
                cluster_id = %self.config.cluster,
                timeout = ?DEREGISTER_TIMEOUT,
                "GDS pool agent deregistration timed out; the agent lease will expire"
            ),
        }
    }
}

impl Drop for GdsAgent {
    fn drop(&mut self) {
        self.stop.cancel();
        if let Some(task) = &self.task {
            task.abort();
        }
    }
}

/// Registers, then follows the served models until `stop`; returns the
/// registration so shutdown can deregister it, `None` when it never
/// registered.
async fn run(
    client: GdsClient,
    config: GdsAgentConfig,
    model_ids: ModelIds,
    stop: CancellationToken,
) -> Option<AgentRegistration> {
    let (registration, mut registered) =
        register_until_accepted(&client, &config, &model_ids, &stop).await?;
    loop {
        if stop
            .run_until_cancelled(tokio::time::sleep(config.model_poll_interval))
            .await
            .is_none()
        {
            return Some(registration);
        }
        let attributes = model_attributes(&model_ids());
        if attributes != registered {
            info!(
                pool = %config.pool_name,
                cluster_id = %config.cluster,
                models = attributes.get(MODELS_ATTRIBUTE).map_or("", String::as_str),
                "served models changed; updating the GDS pool agent"
            );
            registration.set_attributes(attributes.clone());
            registered = attributes;
        }
    }
}

/// `RegisterPoolAgent` with backoff until GDS accepts it or `stop` fires.
/// Returns the registration and the attributes it carried.
async fn register_until_accepted(
    client: &GdsClient,
    config: &GdsAgentConfig,
    model_ids: &ModelIds,
    stop: &CancellationToken,
) -> Option<(AgentRegistration, BTreeMap<String, String>)> {
    let mut failures: u32 = 0;
    loop {
        let spec = config.spec(&model_ids());
        let attributes = spec.attributes.clone();
        // The heartbeat runs until shutdown deregisters it, not until `stop`.
        let registration =
            AgentRegistration::register(client.clone(), spec, CancellationToken::new());
        match stop.run_until_cancelled(registration).await? {
            Ok(registration) => return Some((registration, attributes)),
            Err(error) => {
                let delay = client.policy().backoff.delay(failures);
                failures = failures.saturating_add(1);
                warn!(
                    pool = %config.pool_name,
                    cluster_id = %config.cluster,
                    failures,
                    ?delay,
                    %error,
                    "GDS pool agent registration failed; retrying"
                );
                stop.run_until_cancelled(tokio::time::sleep(delay)).await?;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use std::net::SocketAddr;
    use std::sync::Mutex;

    use gds_client::tonic::{self, Request, Response, Status};
    use gds_client::v1::pool_service_server::{PoolService, PoolServiceServer};
    use gds_client::v1::{
        DeregisterPoolAgentRequest, DeregisterPoolAgentResponse, HeartbeatPoolAgentRequest,
        HeartbeatPoolAgentResponse, Lease, PoolAgentRegistration, PoolState,
        RegisterPoolAgentRequest, RegisterPoolAgentResponse,
    };
    use gds_client::{Backoff, ConnectPolicy, GdsClientConfig};
    use tokio::net::TcpListener;
    use tokio_stream::wrappers::TcpListenerStream;

    use super::*;

    const WAIT: Duration = Duration::from_secs(5);

    /// A GDS that serves only the pool agent RPCs and records them.
    #[derive(Clone, Default)]
    struct FakeAgents {
        state: Arc<Mutex<FakeAgentsState>>,
    }

    #[derive(Default)]
    struct FakeAgentsState {
        reject_registrations: usize,
        epoch: u64,
        registrations: Vec<RegisterPoolAgentRequest>,
        heartbeats: Vec<HeartbeatPoolAgentRequest>,
        deregistrations: Vec<DeregisterPoolAgentRequest>,
    }

    impl FakeAgents {
        fn state(&self) -> std::sync::MutexGuard<'_, FakeAgentsState> {
            self.state.lock().unwrap()
        }

        fn registrations(&self) -> Vec<RegisterPoolAgentRequest> {
            self.state().registrations.clone()
        }

        fn heartbeats(&self) -> Vec<HeartbeatPoolAgentRequest> {
            self.state().heartbeats.clone()
        }

        fn deregistrations(&self) -> Vec<DeregisterPoolAgentRequest> {
            self.state().deregistrations.clone()
        }

        async fn serve(&self) -> SocketAddr {
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let addr = listener.local_addr().unwrap();
            let service = PoolServiceServer::new(self.clone());
            tokio::spawn(
                tonic::transport::Server::builder()
                    .add_service(service)
                    .serve_with_incoming(TcpListenerStream::new(listener)),
            );
            addr
        }

        fn lease(&self, ttl: Option<gds_client::prost_types::Duration>) -> Lease {
            Lease {
                epoch: self.state().epoch,
                ttl,
                ..Lease::default()
            }
        }
    }

    #[tonic::async_trait]
    impl PoolService for FakeAgents {
        async fn register_pool_agent(
            &self,
            request: Request<RegisterPoolAgentRequest>,
        ) -> Result<Response<RegisterPoolAgentResponse>, Status> {
            let request = request.into_inner();
            let lease_ttl = request.lease_ttl;
            let agent = request.agent.clone().unwrap_or_default();
            {
                let mut state = self.state();
                state.registrations.push(request);
                if state.reject_registrations > 0 {
                    state.reject_registrations -= 1;
                    return Err(Status::internal("GDS is not ready"));
                }
                state.epoch += 1;
            }
            let registration = PoolAgentRegistration {
                agent_id: agent.agent_id.clone(),
                lease: Some(self.lease(lease_ttl)),
                attributes: agent.attributes.clone(),
                generation: request_generation(&self.registrations()),
                ..PoolAgentRegistration::default()
            };
            Ok(Response::new(RegisterPoolAgentResponse {
                pool_id: "pool-1".to_string(),
                agent: Some(agent),
                registration: Some(registration),
                pool_state: PoolState::Active as i32,
                server_time: None,
            }))
        }

        async fn heartbeat_pool_agent(
            &self,
            request: Request<HeartbeatPoolAgentRequest>,
        ) -> Result<Response<HeartbeatPoolAgentResponse>, Status> {
            self.state().heartbeats.push(request.into_inner());
            Ok(Response::new(HeartbeatPoolAgentResponse {
                lease: Some(self.lease(None)),
                pool_state: PoolState::Active as i32,
                server_time: None,
            }))
        }

        async fn deregister_pool_agent(
            &self,
            request: Request<DeregisterPoolAgentRequest>,
        ) -> Result<Response<DeregisterPoolAgentResponse>, Status> {
            self.state().deregistrations.push(request.into_inner());
            Ok(Response::new(DeregisterPoolAgentResponse {}))
        }
    }

    fn request_generation(registrations: &[RegisterPoolAgentRequest]) -> u64 {
        registrations.last().map_or(0, |request| request.generation)
    }

    fn client(addr: SocketAddr) -> GdsClient {
        GdsClient::new(GdsClientConfig {
            urls: vec![format!("http://{addr}")],
            policy: ConnectPolicy {
                allow_insecure_http: true,
                backoff: Backoff {
                    initial: Duration::from_millis(5),
                    max: Duration::from_millis(20),
                    multiplier: 2.0,
                },
                ..ConnectPolicy::default()
            },
            ..GdsClientConfig::default()
        })
        .unwrap()
    }

    fn config() -> GdsAgentConfig {
        GdsAgentConfig {
            // Heartbeats every 100 ms.
            lease_ttl: Some(Duration::from_millis(300)),
            model_poll_interval: Duration::from_millis(20),
            ..GdsAgentConfig::new("prod", "cluster-b", "llama", "pylon-0")
        }
    }

    fn served(models: &Arc<Mutex<Vec<String>>>) -> ModelIds {
        let models = models.clone();
        Arc::new(move || models.lock().unwrap().clone())
    }

    async fn wait_until(what: &str, mut condition: impl FnMut() -> bool) {
        tokio::time::timeout(WAIT, async {
            while !condition() {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
        })
        .await
        .unwrap_or_else(|_| panic!("timed out waiting for {what}"));
    }

    fn models(list: &str) -> std::collections::HashMap<String, String> {
        std::collections::HashMap::from([(MODELS_ATTRIBUTE.to_string(), list.to_string())])
    }

    #[test]
    fn model_attribute_lists_sorted_unique_ids() {
        assert!(model_attributes(&[]).is_empty());
        assert!(model_attributes(&[" ".to_string()]).is_empty());
        let ids = ["model-b", "model-a", "model-b"].map(String::from);
        assert_eq!(
            model_attributes(&ids),
            BTreeMap::from([(MODELS_ATTRIBUTE.to_string(), "model-a,model-b".to_string())])
        );
    }

    #[tokio::test]
    async fn registers_follows_models_and_deregisters_on_shutdown() {
        let fake = FakeAgents::default();
        let addr = fake.serve().await;
        let served_models = Arc::new(Mutex::new(vec!["model-a".to_string()]));
        let agent = GdsAgent::start(client(addr), config(), served(&served_models));

        wait_until("the registration", || fake.registrations().len() == 1).await;
        let request = fake.registrations().remove(0);
        assert_eq!(request.clusterset, "prod");
        assert_eq!(request.cluster, "cluster-b");
        assert_eq!(request.pool_name, "llama");
        let registered = request.agent.expect("agent");
        assert_eq!(registered.role(), FrontendRole::Pylon);
        assert_eq!(registered.agent_id, "pylon-0");
        assert_eq!(registered.attributes, models("model-a"));

        served_models.lock().unwrap().push("model-b".to_string());
        wait_until("a heartbeat with the new models", || {
            fake.heartbeats()
                .iter()
                .any(|heartbeat| heartbeat.attributes == models("model-a,model-b"))
        })
        .await;
        assert_eq!(fake.registrations().len(), 1);

        agent.shutdown().await;
        let deregistrations = fake.deregistrations();
        assert_eq!(deregistrations.len(), 1);
        assert_eq!(deregistrations[0].clusterset, "prod");
        assert_eq!(deregistrations[0].pool_id, "pool-1");
        assert_eq!(deregistrations[0].role(), FrontendRole::Pylon);
        assert_eq!(deregistrations[0].agent_id, "pylon-0");
        assert!(
            fake.heartbeats()
                .iter()
                .all(|heartbeat| heartbeat.agent_id == "pylon-0"),
            "heartbeats name the inference server id"
        );
        let heartbeats = fake.heartbeats().len();
        tokio::time::sleep(Duration::from_millis(300)).await;
        assert_eq!(
            fake.heartbeats().len(),
            heartbeats,
            "no heartbeat after shutdown"
        );
    }

    #[tokio::test]
    async fn registration_retries_until_gds_accepts_it() {
        let fake = FakeAgents::default();
        fake.state().reject_registrations = 2;
        let addr = fake.serve().await;
        let agent = GdsAgent::start(client(addr), config(), served(&Arc::default()));

        wait_until("a heartbeat", || !fake.heartbeats().is_empty()).await;
        assert_eq!(fake.registrations().len(), 3);
        assert!(
            fake.registrations().iter().all(|request| request
                .agent
                .as_ref()
                .is_some_and(|agent| agent.attributes.is_empty())),
            "no models attribute while no model is served"
        );
        agent.shutdown().await;
        assert_eq!(fake.deregistrations().len(), 1);
    }

    #[tokio::test]
    async fn shutdown_before_registration_does_not_deregister() {
        let fake = FakeAgents::default();
        fake.state().reject_registrations = usize::MAX;
        let addr = fake.serve().await;
        let agent = GdsAgent::start(client(addr), config(), served(&Arc::default()));

        wait_until("a rejected registration", || {
            !fake.registrations().is_empty()
        })
        .await;
        tokio::time::timeout(WAIT, agent.shutdown())
            .await
            .expect("shutdown should not wait for GDS");
        assert!(fake.deregistrations().is_empty());
    }
}
