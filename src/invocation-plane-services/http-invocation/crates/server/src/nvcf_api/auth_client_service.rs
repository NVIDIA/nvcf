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

use crate::nvcf_api::oauth2_client::TokenProducer;
use anyhow::Context as anyhowContext;
use http::header::AUTHORIZATION;
use http::{Request, Response};
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};
use tonic::body::Body;
use tonic::transport::Channel;
use tower::{Layer, Service};

#[derive(Clone)]
pub struct AuthLayer {
    token_provider: Arc<Option<TokenProducer>>,
}

impl AuthLayer {
    pub fn new(token_provider: Option<TokenProducer>) -> Self {
        Self {
            token_provider: token_provider.into(),
        }
    }
}

impl Layer<Channel> for AuthLayer {
    type Service = AuthSvc;

    fn layer(&self, service: Channel) -> Self::Service {
        AuthSvc::new(service, self.token_provider.clone())
    }
}

// using this example https://github.com/hyperium/tonic/blob/master/examples/src/tower/client.rs
#[derive(Clone)]
pub struct AuthSvc {
    inner: Channel,
    token_provider: Arc<Option<TokenProducer>>,
}

impl AuthSvc {
    pub fn new(inner: Channel, token_provider: Arc<Option<TokenProducer>>) -> Self {
        AuthSvc {
            inner,
            token_provider,
        }
    }
}

#[derive(thiserror::Error, Debug)]
pub enum Error {
    /// Error from the underlying tonic grpc client
    #[error("GRPC error: {0}")]
    Transport(#[from] tonic::transport::Error),
    /// There was an error from something else
    #[error("Other error: {0}")]
    Other(#[from] anyhow::Error),
}

impl Service<Request<Body>> for AuthSvc {
    type Response = Response<Body>;
    type Error = Error;
    #[allow(clippy::type_complexity)]
    type Future = Pin<Box<dyn Future<Output = Result<Self::Response, Self::Error>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx).map_err(Into::into)
    }

    fn call(&mut self, mut req: Request<Body>) -> Self::Future {
        // This is necessary because tonic internally uses `tower::buffer::Buffer`.
        // See https://github.com/tower-rs/tower/issues/547#issuecomment-767629149
        // for details on why this is necessary
        let clone = self.inner.clone();
        let mut inner = std::mem::replace(&mut self.inner, clone);
        let token_provider = self.token_provider.clone();
        Box::pin(async move {
            let req = match token_provider.as_ref() {
                Some(token_provider) => {
                    let access_token = token_provider.produce_token().await?;
                    let authorization_header = format!("Bearer {access_token}")
                        .parse()
                        .context("could not format grpc authorization header")?;
                    req.headers_mut()
                        .insert(AUTHORIZATION, authorization_header);
                    req
                }
                None => req,
            };

            let response = inner.call(req).await?;
            Ok(response)
        })
    }
}
