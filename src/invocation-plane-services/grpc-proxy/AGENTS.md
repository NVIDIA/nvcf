# AGENTS.md - grpc-proxy

Native Go image-source subtree for the NVCF gRPC proxy. The service handles
HTTP/3 CONNECT, HTTP/1 CONNECT, and HTTP/1-2 forwarding for gRPC requests.

## Build and Test

Bazel is the canonical build path. The Dockerfile and direct Go flow remain
available for local iteration.

```bash
bazel build //...
bazel test //... --flaky_test_attempts=3
bazel build //:image_index
bazel run //:gazelle
bazel mod tidy
```

CI subproject id: `grpc-proxy`. Follow the root `RELEASE.md` and
`docs/dev/github-release-process.md` for source versioning and publication
handoffs. Inspect `.github/workflows/` for current public validation.

## Local Gotchas

- Session routing assumes requests on the same TCP connection with identical
  function routing headers belong to one stateful session.
- Browser websocket clients pass metadata through `Sec-WebSocket-Protocol`
  because they cannot set arbitrary headers.
- Regenerate BUILD files with Gazelle after adding Go files or imports.
- Changes to public invocation behavior may need follow-up in
  `src/clis/nvcf-cli`.
