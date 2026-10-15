# CRIU Lazy Pages Spike (Issue #37)

## Status: NOT VIABLE

Tested 2026-03-04. Lazy pages is not viable for GPU workloads on our infrastructure.

## What is Lazy Pages?

CRIU lazy pages uses Linux `userfaultfd` to restore processes with demand-paged memory. Instead of loading all pages at restore time, pages are faulted in on first access via a userspace page server. This could theoretically give near-instant restore times.

## Test Results

### Kernel Support: FAILED

- **GKE kernel 5.15**: `unprivileged_userfaultfd = 0` (disabled)
- `criu check --feature lazy_pages` exits with code 1
- userfaultfd requires either `CAP_SYS_PTRACE` in the *restored* process's namespace or `unprivileged_userfaultfd = 1` (a sysctl we don't control on GKE)

### Why It Won't Work for GPU Workloads (Even With Kernel Support)

Three independent blockers:

1. **VMA type restriction**: `vma_entry_can_be_lazy()` in CRIU only allows `MAP_PRIVATE | MAP_ANONYMOUS` VMAs. GPU device memory is device file-backed (`/dev/nvidia*`), so it's excluded from lazy loading.

2. **CUDA plugin architecture**: During restore, the CUDA plugin replaces NVIDIA device mmaps with `/dev/zero`, then `cuda-checkpoint --action restore` eagerly restores all GPU state in one shot. There's no mechanism for lazy GPU memory faulting.

3. **userfaultfd limitation**: userfaultfd can only inject regular pages via `UFFDIO_COPY`. It cannot trigger device driver ioctls needed to restore GPU memory mappings.

### Page Split Analysis

Even in the best case (lazy pages working for CPU memory only):
- GPU memory dominates checkpoint size for inference workloads
- vLLM TinyLlama: ~26GB GPU vs ~2GB CPU pages — lazy loading CPU pages saves <8% of restore I/O
- For larger models (8B, 70B), the ratio is even more GPU-heavy

## Test Script

`scripts/test-lazy-pages.sh` — runs three progressive tests:
1. CPU-only baseline (verifies kernel/CRIU support)
2. GPU workload lazy restore attempt
3. Page split analysis of existing checkpoints

## Conclusion

Close #37. Proceed with transparent compression (#46) which provides 8x reduction in checkpoint I/O — a much larger and more reliable benefit than lazy pages could offer even in the best case.
