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

use crate::worker_streams::new_from_buf_and_body_data_stream;
use axum::body::{Body, HttpBody};
use bytes::{Bytes, BytesMut};
use futures::{FutureExt, TryStreamExt};

const MAX_BUFFER_SIZE: u64 = 32 * 1024;

/// Prepares the request body for further processing.
///
/// This function reads the initial chunk of the request body (up to 32KB) and determines if the end of the file (EOF) has been reached.
/// If EOF is reached, it returns the read bytes without setting up a stream for further reads.
/// If EOF is not reached, indicating a potential for a streaming request body or more data to come, it sets up a stream for further reads.
///
/// # Returns
///
/// - `Bytes`: The initial chunk of the request body (up to 32KB).
/// - `Option<Body>`:
///   - `None` if EOF is reached after the initial read.
///   - `Some(Body)` if EOF is not reached, indicating more data may be available for streaming.
///     - `Body` will have a size hint if the request body has a size hint.
pub async fn prepare_body(body: Body) -> anyhow::Result<(Bytes, Option<Body>)> {
    // Determine the initial buffer size to read, maxing out at 32KB if the body size is unknown or larger.
    let size_hint = body.size_hint().exact();
    let initial_buffer_size = match size_hint {
        None => MAX_BUFFER_SIZE,
        Some(exact) if exact >= MAX_BUFFER_SIZE => MAX_BUFFER_SIZE,
        Some(exact) => exact,
    };
    let mut buf = BytesMut::with_capacity(initial_buffer_size as usize);

    // Read the body into the buffer, up to its capacity.
    let mut body = body.into_data_stream();
    let mut overflow = None;

    // don't block waiting for bytes if we don't have a target read size.
    // the request body might be a stream that won't start sending until it gets a response.
    if size_hint.is_some() {
        while let Some(bytes) = body.try_next().await? {
            fill_buf_with_overflow(&mut buf, &mut overflow, bytes);
            if buf.capacity() == buf.len() {
                break;
            }
        }
    }
    // if there is space left in the first chunk, read what we can without blocking for IO
    if buf.capacity() > buf.len() {
        while let Some(Some(bytes)) = body
            .try_next()
            .now_or_never() // only read what is currently available
            .transpose()?
        {
            fill_buf_with_overflow(&mut buf, &mut overflow, bytes);
            if buf.capacity() == buf.len() {
                break;
            }
        }
    }

    let first_chunk = buf.freeze();

    // Check if the end of the stream has been reached after the initial read.
    let is_eof = overflow.is_none() && body.is_end_stream();

    // If EOF is reached, return the read bytes without setting up a stream.
    let stream = if is_eof {
        None
    } else {
        // If EOF is not reached, set up a stream for the remaining body.
        Some(if let Some(overflow) = overflow {
            new_from_buf_and_body_data_stream(overflow, body)?
        } else {
            Body::new(body)
        })
    };

    Ok((first_chunk, stream))
}

fn fill_buf_with_overflow(buf: &mut BytesMut, overflow: &mut Option<Bytes>, mut bytes: Bytes) {
    let remaining_space = buf.capacity() - buf.len();
    if remaining_space < bytes.len() {
        let to_buf = bytes.split_to(remaining_space);
        buf.extend_from_slice(&to_buf);
        _ = overflow.insert(bytes);
    } else {
        buf.extend_from_slice(&bytes);
    }
}

/// Tests use a full axum server and reqwest library to simulate actual network behaviour.
/// Sending a manually constructed body does not set a size hint.
#[cfg(test)]
mod tests {
    use super::*;
    use axum::routing::post;
    use axum::Router;
    use bytes::Bytes;
    use futures::{StreamExt, TryStreamExt};
    use http::header::CONTENT_LENGTH;
    use http::StatusCode;
    use http_body_util::BodyExt;
    use scopeguard::defer;
    use std::io::Error;
    use std::pin::pin;
    use std::time::Duration;
    use tokio::net::TcpListener;
    use tokio_stream::once;

    #[tokio::test]
    async fn test_prepare_body() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, "abc123");
                assert!(stream.is_none());
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! {server.abort()}

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body("abc123")
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_streaming_body() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, "abc");

                // Verify the streaming body
                assert!(stream.is_some());
                let mut stream = pin!(stream.unwrap().into_data_stream());
                assert!(!stream.is_end_stream());
                assert!(stream.size_hint().exact().is_none());
                let mut aggregated = Vec::new();
                while let Some(item) = stream.try_next().await.unwrap() {
                    aggregated.extend_from_slice(&item);
                }
                assert_eq!(aggregated, "123".as_bytes());

                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        // Create a streaming body
        let stream = async_stream::stream! {
            yield Bytes::from("abc");
            tokio::time::sleep(Duration::from_millis(10)).await;
            yield "123".into();
        }
        .map(Ok::<_, Error>);

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(reqwest::Body::wrap_stream(stream))
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_buffered_body() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, "abc123");
                // Verify the streaming body
                assert!(stream.is_none());
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        // Create a streaming body
        let stream = async_stream::stream! {
            yield Bytes::from("abc");
            yield "123".into();
        }
        .map(Ok::<_, Error>);

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(reqwest::Body::wrap_stream(stream))
            .header(CONTENT_LENGTH, 6)
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_buffered_no_content_length() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, "abc123");
                // Verify the streaming body
                assert!(stream.is_some());
                let data_stream = stream.unwrap().into_data_stream();
                assert!(!data_stream.is_end_stream());
                assert!(data_stream.size_hint().exact().is_none());
                let out = BodyExt::collect(data_stream).await.unwrap().to_bytes();
                assert!(out.is_empty());
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        // Create a streaming body
        let stream = once(Ok::<_, Error>(Bytes::from("abc123")));

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(reqwest::Body::wrap_stream(stream))
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_prepare_body_overflow_initial_buffer() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, vec![0; 32 * 1024]);
                // Verify the streaming body
                let data_stream = stream.unwrap().into_data_stream();
                assert!(!data_stream.is_end_stream());
                assert_eq!(data_stream.size_hint().exact(), Some(10));
                let remainder = BodyExt::collect(data_stream).await.unwrap().to_bytes();
                assert_eq!(remainder, vec![0; 10]);
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(vec![0; 32 * 1024 + 10])
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_prepare_body_multiple_stream_segments() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, "123");
                // Verify the streaming body
                let data_stream = stream.unwrap().into_data_stream();
                assert!(!data_stream.is_end_stream());
                assert!(data_stream.size_hint().exact().is_none());
                let remainder = BodyExt::collect(data_stream).await.unwrap().to_bytes();
                assert_eq!(remainder, "456789".as_bytes());
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        // Create a streaming body
        let stream = async_stream::stream! {
            yield Bytes::new();
            yield "123".into();
            yield "456".into();
            yield "789".into();
        }
        .map(Ok::<_, Error>);

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(reqwest::Body::wrap_stream(stream))
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }

    #[tokio::test]
    async fn test_prepare_body_empty_request() -> Result<(), anyhow::Error> {
        let app = Router::new().route(
            "/",
            post(|req: http::Request<Body>| async move {
                let (parts, body) = req.into_parts();
                dbg!(parts);
                let (first_chunk, stream) = prepare_body(body).await.unwrap();
                assert_eq!(first_chunk, Bytes::new());
                // Verify the streaming body
                assert!(stream.is_none());
                StatusCode::OK
            }),
        );

        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;
        let server = tokio::spawn(async move { axum::serve(listener, app).await });
        defer! { server.abort() }

        let response = reqwest::Client::new()
            .post(format!("http://{}/", addr))
            .body(reqwest::Body::default())
            .send()
            .await?;
        assert_eq!(response.status(), StatusCode::OK);

        Ok(())
    }
}
