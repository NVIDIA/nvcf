# Simulation Cluster Caches

This section covers cache components for self-hosted NVCF deployments. Caches improve performance by storing frequently accessed content locally, reducing network bandwidth usage and accelerating scene loading.

## Overview

Self-hosted NVCF supports several cache components:

- Derived Data Cache Service (DDCS): caches derived content to reduce scene load time and improve rendering performance
- USD Content Cache (UCC): caches USD content from object storage to accelerate scene loading

## When to Use Caches

See the individual cache component guides for detailed information on when to use each cache:

- [Derived Data Cache Service](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/) - Derived Data Cache Service
- [USD Content Cache](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/) - USD Content Cache

## Documentation

Each cache component has comprehensive documentation covering configuration, deployment, and advanced features.

### DDCS documentation

- [DDCS](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/)
- [DDCS Configuration](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/configure.html)
- [DDCS Deployment](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/deploy.html)
- [DDCS TLS](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/tls.html)

### UCC documentation

- [UCC](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/)
- [UCC Configuration](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/configure.html)
- [UCC Deployment](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/deploy.html)
- [UCC TLS](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/tls.html)

Operational procedures for both caches are in the [Cache Runbook](./runbooks/caches.md).

## Configuration

Cache components are configured using Helm values files. Each cache guide includes:

- Base configuration examples
- Configuration options and parameters
- Performance tuning recommendations
- Best practices

## Monitoring

Cache components include Prometheus metrics for monitoring:

- Cache hit ratios
- Storage utilization
- Request throughput
- Performance metrics

## Next Steps

1. Review the cache guides above and decide which caches fit your use case.
2. Configure Helm values files based on the vendor examples.
3. Deploy the caches with Helm or Helmfile.
4. Set up monitoring dashboards and alerts.
