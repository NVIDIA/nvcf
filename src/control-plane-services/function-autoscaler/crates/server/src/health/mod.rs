/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use crate::metrics;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fmt;
use std::sync::{Arc, RwLock};
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::time::Duration;
use tracing;

/// Health status enumeration representing the current state of the service
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum HealthStatus {
    Healthy,
    Unhealthy,
}

impl fmt::Display for HealthStatus {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            HealthStatus::Healthy => write!(f, "healthy"),
            HealthStatus::Unhealthy => write!(f, "unhealthy"),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthCheckResult {
    pub is_healthy: bool,
    pub message: Option<String>,
    pub last_updated: u64, // Unix timestamp
}

impl Default for HealthCheckResult {
    fn default() -> Self {
        Self {
            is_healthy: true,
            message: Some("Health check passed".to_string()),
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs(),
        }
    }
}

/// Health information for a specific component
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ComponentHealth {
    pub status: HealthStatus,
    pub last_updated: u64, // Unix timestamp
    pub message: Option<String>,
}

impl ComponentHealth {
    pub fn new(status: HealthStatus, message: Option<String>) -> Self {
        Self {
            status,
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs(),
            message,
        }
    }
}

/// Comprehensive health information including overall status and per-component health
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthInfo {
    pub overall_status: HealthStatus,
    pub last_updated: u64, // Unix timestamp
    pub message: Option<String>,
    pub components: HashMap<String, ComponentHealth>,
}

impl Default for HealthInfo {
    fn default() -> Self {
        Self {
            overall_status: HealthStatus::Healthy,
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs(),
            message: Some("Service starting up".to_string()),
            components: HashMap::new(),
        }
    }
}

impl HealthInfo {
    /// Recalculate overall health status based on component healths
    fn update_overall_status(&mut self) {
        if self.components.is_empty() {
            self.overall_status = HealthStatus::Healthy;
            self.message = Some("No components registered".to_string());
            return;
        }

        let mut healthy_count = 0;
        let mut unhealthy_count = 0;
        let mut messages = Vec::new();

        for (component_name, component_health) in &self.components {
            match component_health.status {
                HealthStatus::Healthy => healthy_count += 1,
                HealthStatus::Unhealthy => {
                    unhealthy_count += 1;
                    if let Some(msg) = &component_health.message {
                        messages.push(format!("{}: {}", component_name, msg));
                    } else {
                        messages.push(format!("{}: unhealthy", component_name));
                    }
                }
            }
        }

        // Overall status logic: healthy only if ALL components are healthy
        if unhealthy_count > 0 {
            self.overall_status = HealthStatus::Unhealthy;
            self.message = Some(format!(
                "{} component(s) unhealthy: {}",
                unhealthy_count,
                messages.join(", ")
            ));
        } else {
            self.overall_status = HealthStatus::Healthy;
            self.message = Some(format!("All {} component(s) healthy", healthy_count));
        }

        self.last_updated = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs();

        // Publish metrics immediately when health status changes
        let overall_healthy = self.overall_status == HealthStatus::Healthy;
        metrics::record_health_overall_status(overall_healthy);

        for (component_name, component_health) in &self.components {
            let component_healthy = component_health.status == HealthStatus::Healthy;
            metrics::record_health_component_status(component_name, component_healthy);
        }
    }
}

#[derive(Clone)]
pub struct Health {
    inner: Arc<RwLock<HealthInfo>>,
    health_checkers: Arc<RwLock<Vec<Arc<dyn HealthChecker>>>>,
}

impl Health {
    /// Create a new Health instance with default unknown status
    pub fn new() -> Self {
        Self {
            inner: Arc::new(RwLock::new(HealthInfo::default())),
            health_checkers: Arc::new(RwLock::new(Vec::new())),
        }
    }

    /// Register a new component for health tracking
    ///
    /// Components start with Unknown status until explicitly set
    pub fn register_component(&self, component_name: &str) {
        let mut health = self.inner.write().unwrap();
        health.components.insert(
            component_name.to_string(),
            ComponentHealth::new(HealthStatus::Unhealthy, Some("initializing".to_string())),
        );
        health.update_overall_status();
    }

    /// Set the health status for a specific component
    pub fn set_component_health(
        &self,
        component_name: &str,
        status: HealthStatus,
        message: Option<String>,
    ) {
        let mut health = self.inner.write().unwrap();

        if let Some(component) = health.components.get_mut(component_name) {
            component.status = status;
            component.message = message;
            component.last_updated = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs();
        } else {
            // Auto-register component if not found
            health.components.insert(
                component_name.to_string(),
                ComponentHealth::new(status, message),
            );
        }

        health.update_overall_status();
    }

    /// Get health status for a specific component
    pub fn get_component_health(&self, component_name: &str) -> Option<ComponentHealth> {
        let health = self.inner.read().unwrap();
        health.components.get(component_name).cloned()
    }

    /// Get the current overall health information
    ///
    /// Returns a clone of the current HealthInfo with aggregated status
    pub fn get_health(&self) -> HealthInfo {
        let health = self.inner.read().unwrap();
        health.clone()
    }

    /// Convenience method to set a component status to healthy
    pub fn set_component_healthy(&self, component_name: &str, message: Option<String>) {
        self.set_component_health(component_name, HealthStatus::Healthy, message);
    }

    /// Convenience method to set a component status to unhealthy
    pub fn set_component_unhealthy(&self, component_name: &str, message: Option<String>) {
        self.set_component_health(component_name, HealthStatus::Unhealthy, message);
    }

    /// Register a health checker service and immediately run one check to set the initial state.
    /// Subsequent checks run on the background monitoring interval (every 30s).
    pub fn register_health_checker(&self, checker: Arc<dyn HealthChecker>) {
        let component_name = checker.component_name().to_string();

        // Register the component as unhealthy until proven otherwise
        self.register_component(&component_name);

        // Add to health checkers list
        {
            let mut checkers = self.health_checkers.write().unwrap();
            checkers.push(checker.clone());
        }

        // Run one check in the background so the state reflects reality before the
        // monitoring loop has had a chance to tick.
        let health = self.clone();
        tokio::spawn(async move {
            let result = checker.health().await;
            if result.is_healthy {
                health.set_component_healthy(&component_name, result.message);
            } else {
                health.set_component_unhealthy(&component_name, result.message);
            }
        });
    }

    /// Start the background health monitoring task
    /// This should be called once after all health checkers are registered
    pub fn start_health_monitoring(self: Arc<Self>, check_interval: Duration) {
        let health = Arc::clone(&self);
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(check_interval);
            interval.tick().await; // Skip first immediate tick

            loop {
                interval.tick().await;

                // Get all registered health checkers
                let checkers = {
                    let checkers_guard = health.health_checkers.read().unwrap();
                    checkers_guard.clone()
                };

                // Check health for each registered service
                for checker in checkers {
                    let component_name = checker.component_name();

                    match checker.health().await {
                        HealthCheckResult {
                            is_healthy: true,
                            message: Some(message),
                            last_updated: _,
                        } => {
                            health.set_component_healthy(component_name, Some(message.clone()));
                            tracing::debug!(
                                "Health check passed for component: {} with message: {}",
                                component_name,
                                message.clone()
                            );
                        }
                        HealthCheckResult {
                            is_healthy: false,
                            message: Some(message),
                            last_updated: _,
                        } => {
                            health.set_component_unhealthy(component_name, Some(message.clone()));
                            tracing::warn!(
                                "Health check failed for component: {} with message: {}",
                                component_name,
                                message.clone()
                            );
                        }
                        _ => {
                            health.set_component_unhealthy(
                                component_name,
                                Some("Health check failed".to_string()),
                            );
                            tracing::warn!("Health check failed for component: {}", component_name);
                        }
                    }
                }
            }
        });
    }
}

impl Default for Health {
    fn default() -> Self {
        Self::new()
    }
}

/// Trait for services that can provide health status
#[async_trait]
pub trait HealthChecker: Send + Sync {
    async fn health(&self) -> HealthCheckResult;
    fn component_name(&self) -> &str;
}

#[cfg(test)]
mod tests {
    use super::*;

    struct MockHealthChecker {
        status: HealthStatus,
    }

    impl MockHealthChecker {
        fn new() -> Self {
            Self {
                status: HealthStatus::Healthy,
            }
        }
    }
    #[async_trait]
    impl HealthChecker for MockHealthChecker {
        async fn health(&self) -> HealthCheckResult {
            HealthCheckResult {
                is_healthy: self.status == HealthStatus::Healthy,
                message: Some("Mocked health check".to_string()),
                last_updated: SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_secs(),
            }
        }
        fn component_name(&self) -> &str {
            "mock_component"
        }
    }

    #[tokio::test]
    async fn test_health_checker() {
        let health = Arc::new(Health::new());
        let health_clone = health.clone();
        health.register_health_checker(Arc::new(MockHealthChecker::new()));
        health.start_health_monitoring(Duration::from_secs(1));
        // Give the spawned initial check task a moment to complete.
        tokio::time::sleep(Duration::from_millis(50)).await;
        let health_info = health_clone.get_health();
        assert_eq!(health_info.overall_status, HealthStatus::Healthy);
        assert_eq!(
            health_info.components.get("mock_component").unwrap().status,
            HealthStatus::Healthy
        );
    }
}
