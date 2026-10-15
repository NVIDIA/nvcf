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

## Physical Simulation Caches

For an overview refer to [self-hosted-caches](./caches.md)

- [Derived Data Cache Service](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/) - Derived Data Cache Service
- [USD Content Cache](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/) - USD Content Cache

<Note>
These enhancements are supported for single-cluster Control Plane and GPU-only (BYOC) clusters.

</Note>
