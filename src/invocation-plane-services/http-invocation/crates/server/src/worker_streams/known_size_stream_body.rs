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

use axum::body::Body;
use bytes::Bytes;
use pin_project_lite::pin_project;
use std::{num::TryFromIntError, ops::SubAssign};
use tokio_stream::once;

pin_project! {
    pub struct KnownSizeStreamBody<S> {
        #[pin]
        stream: sync_wrapper::SyncWrapper<S>,
        size: u64,
    }
}

/// KnownSizeStreamBody is largely copied from axum::body::Body::from_stream() but also includes a size hint
impl<S> KnownSizeStreamBody<S> {
    pub fn new(stream: S, size: u64) -> Self {
        Self {
            stream: sync_wrapper::SyncWrapper::new(stream),
            size,
        }
    }
}

impl<S> axum::body::HttpBody for KnownSizeStreamBody<S>
where
    S: futures::TryStream,
    S::Ok: Into<Bytes>,
    S::Error: Into<axum::BoxError>,
{
    type Data = Bytes;
    type Error = axum::Error;

    fn poll_frame(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Result<hyper::body::Frame<Self::Data>, Self::Error>>> {
        let this = self.project();
        let stream = this.stream.get_pin_mut();
        match futures::ready!(stream.try_poll_next(cx)) {
            Some(Ok(chunk)) => {
                let chunk = chunk.into();
                let len: u64 = chunk.len().try_into().map_err(axum::Error::new)?;
                this.size.sub_assign(len);
                std::task::Poll::Ready(Some(Ok(hyper::body::Frame::data(chunk))))
            }
            Some(Err(err)) => std::task::Poll::Ready(Some(Err(axum::Error::new(err)))),
            None => std::task::Poll::Ready(None),
        }
    }

    fn is_end_stream(&self) -> bool {
        self.size == 0
    }

    fn size_hint(&self) -> hyper::body::SizeHint {
        hyper::body::SizeHint::with_exact(self.size)
    }
}

/// we need to pass the content length through any time we have a known content length.
/// when we wrap data streams with known content length and put them in streams with unknown
/// content length, the upstream http clients will not always read to EOF and instead read to the
/// known inner content length. this will prevent connection reuse as the outer stream is still
/// waiting to send the final EOF chunk. by ensuring our outer stream has a known content length we
/// do not use Transfer-Encoding: chunked so there is no explicit EOF chunk needed. when the content
/// length has been read, both sides know the stream is finished without reading to EOF allowing the
/// connection to be reused. this only applies to HTTP1.1 connection reuse but is good practice to
/// pass through the content length if we know it anyway.
pub fn new_from_buf_and_body_data_stream(
    buf: Bytes,
    body_data_stream: axum::body::BodyDataStream,
) -> Result<Body, TryFromIntError> {
    let overflow_len: u64 = buf.len().try_into()?;
    let body_size_hint = axum::body::HttpBody::size_hint(&body_data_stream).exact();
    let body_stream = tokio_stream::StreamExt::chain(once(Ok(buf)), body_data_stream);
    Ok(if let Some(exact) = body_size_hint {
        let limit = exact + overflow_len;
        Body::new(KnownSizeStreamBody::new(body_stream, limit))
    } else {
        Body::from_stream(body_stream)
    })
}
