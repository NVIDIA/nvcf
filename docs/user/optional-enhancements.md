# Optional Enhancements

NVCF supports several optional components that can enhance your deployment's
performance, routing, and GPU cluster capabilities. Each component has its own
installation and configuration guide.

## Low-Latency Streaming

- [LLS Installation](lls-installation.md) - Required for streaming Cloud Functions using WebRTC

## LLM Functions

- [LLM Function Enablement](./llm-function-enablement.md) - Required for `functionType: "LLM"` functions using the LLM invocation gateway

## NVCF Caches

- [container-cache](./cluster-management/container-cache.md) - Accelerates container image pulls by caching layers locally
- [gxcache](./cluster-management/gxcache.md) - Shader caching for simulation and rendering workloads

## Vanity Gateway

Vanity Gateway is an optional HTTP gateway service for deployments that need
customer-facing hostnames or path mappings in front of the standard NVCF API and
invocation routes. It is available only in stack packages that include the
Vanity Gateway addon. Older packages do not contain the `vanity-gateway` release
or route values.

When the addon is present and enabled, it is deployed as the `vanity-gateway`
service and exposed through the Gateway API route `vanity.<domain>` by default.
Enable it only when you need a vanity routing layer. Standard API, API Keys,
invocation, LLM invocation, and gRPC routes do not require it. See
[Gateway Routing](./gateway-routing.md#vanity-gateway-optional) for routing and
verification details.

## NVCF UI

NVCF UI is an optional addon that deploys a customer-facing NVCF admin-panel UI. 
It is available only in stack packages that include the
NVCF UI addon. Older packages do not contain the `nvcf-ui` release
or route values.

When the addon is present and enabled, it is deployed as the `nvcf-ui`
service and exposed through the Gateway API route `nvcf-ui.<domain>` by default.
See [Gateway Routing](./gateway-routing.md#nvcf-ui-optional) for routing and
verification details.

## Physical Simulation Caches

For an overview refer to [self-hosted-caches](./caches.md)

- [Derived Data Cache Service](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/) - Derived Data Cache Service
- [USD Content Cache](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/) - USD Content Cache

<Note>
These enhancements are supported for single-cluster Control Plane and GPU-only (BYOC) clusters.

</Note>
