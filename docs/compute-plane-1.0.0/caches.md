# Simulation Cluster Caches

This section covers cache components for self-hosted NVCF deployments. Caches improve performance by storing frequently accessed content locally, reducing network bandwidth usage and accelerating scene loading.

## Overview

Self-hosted NVCF supports several cache components:

- **Derived Data Cache Service (DDCS)** - Caches derived content to reduce scene load time and improve rendering performance
- **USD Content Cache (UCC)** - Caches USD content from object storage to accelerate scene loading

## When to Use Caches

See the individual cache component guides for detailed information on when to use each cache:

- [Derived Data Cache Service](https://docs.omniverse.nvidia.com/ovcaches/ddcs/5.0/) - Derived Data Cache Service
- [USD Content Cache](https://docs.omniverse.nvidia.com/ovcaches/ucc/3.0/) - USD Content Cache

## Documentation

Each cache component has comprehensive documentation covering configuration, deployment, and advanced features:

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

1. **Review cache guides** - Read the detailed guides for caches you want to deploy
2. **Plan your deployment** - Determine which caches fit your use case
3. **Configure caches** - Set up Helm values files based on the examples
4. **Deploy caches** - Install caches using Helm or Helmfile
5. **Monitor performance** - Set up monitoring dashboards and alerts
