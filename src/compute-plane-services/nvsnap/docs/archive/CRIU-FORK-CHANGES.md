# CRIU Fork Changes Documentation

This document lists all modifications made to the CRIU fork (`github.com/balaji-g/criu`, branch `criu-dev`) for GPU checkpoint/restore support.

## Summary

Total custom commits: **26** (on top of upstream `criu-dev`)

Changes are grouped by feature area. Each can be reverted independently by reverting its commit(s).

---

## 1. /dev/shm Ghost File Handling (CRITICAL)

### Purpose
Fix checkpoint failures caused by POSIX semaphores in `/dev/shm` that appear as "deleted" but still exist.

### Commits
| Commit | Description |
|--------|-------------|
| `bd1ea9960` | **fix: Skip /dev/shm ghost files during dump when using --skip-mnt-ns** |
| `1c111178d` | fix: Handle /dev/shm ghost files properly for container restore |

### Files Modified
- `criu/files-reg.c` - Skip logic for `/dev/shm` ghost files when `EEXIST` occurs

### Why Needed
vLLM creates POSIX semaphores that get deleted but remain open. CRIU's `linkat()` fails with `EEXIST`. This fix skips these files during dump since they're runtime objects.

### How to Revert
```bash
git revert bd1ea9960 1c111178d
```

---

## 2. --skip-mnt-ns Flag (CRITICAL for Kubernetes)

### Purpose
Skip mount namespace handling during dump/restore for container migration where mounts are managed by the container runtime.

### Commits
| Commit | Description |
|--------|-------------|
| `1274d4425` | Add --skip-mnt-ns and --skip-file-validation flags for K8s container restore |
| `0e237c94c` | Add --skip-mnt-ns enhancements for container migration |
| `d2f070022` | feat: add dump-side --skip-mnt-ns support for Kubernetes/NVIDIA |

### Files Modified
- `criu/config.c` - Option registration
- `criu/include/cr_options.h` - Option struct
- `criu/mount.c`, `criu/mount-v2.c` - Mount handling skip
- `criu/files-reg.c` - File path handling
- `criu/eventpoll.c` - Epoll handling

### Why Needed
Kubernetes containers have complex mount topologies managed by containerd/CRI-O. Trying to recreate them causes failures. Skip and let the runtime handle it.

### How to Revert
```bash
git revert d2f070022 0e237c94c 1274d4425
```

---

## 3. --portable Flag

### Purpose
Save all file-backed pages in checkpoint images for cross-node migration where original files may not exist.

### Commits
| Commit | Description |
|--------|-------------|
| `a107570cf` | Add --portable flag for cross-node checkpoint migration |

### Files Modified
- `criu/config.c` - Option registration
- `criu/include/cr_options.h` - Option struct
- `criu/sk-inet.c`, `criu/sk-unix.c` - Socket handling

### Why Needed
When restoring on a different node, JIT-compiled `.so` files and cache files won't exist. `--portable` saves all page data so the process can resume without the original files.

### How to Revert
```bash
git revert a107570cf
```

---

## 4. --skip-missing-files Flag

### Purpose
Continue restore even if some file paths don't exist (convert to anonymous mappings).

### Commits
| Commit | Description |
|--------|-------------|
| `b080f7364` | feat: Implement --skip-missing-files flag for container migration |

### Files Modified
- `criu/files-reg.c`

### Why Needed
When restoring on a different container, some paths (like `/root/.cache/...`) may not exist. Instead of failing, convert those mappings to anonymous memory.

### How to Revert
```bash
git revert b080f7364
```

---

## 5. --skip-fsnotify Flag

### Purpose
Skip dumping inotify/fanotify file descriptors.

### Commits
| Commit | Description |
|--------|-------------|
| `59b4847b3` | Add --skip-fsnotify option to skip inotify/fanotify dump |

### Files Modified
- `criu/config.c`
- `criu/fsnotify.c`
- `criu/include/cr_options.h`

### Why Needed
File system notification watchers often can't be meaningfully restored across nodes/containers.

### How to Revert
```bash
git revert 59b4847b3
```

---

## 6. --skip-lsm Flag

### Purpose
Skip Linux Security Module (SELinux/AppArmor) context verification during restore.

### Commits
| Commit | Description |
|--------|-------------|
| `da39a70eb` | feat: add --skip-lsm flag for container migration |

### Files Modified
- `criu/config.c`
- `criu/include/cr_options.h`
- `criu/lsm.c`

### Why Needed
LSM contexts may differ between source and target containers. Skip verification to allow restore.

### How to Revert
```bash
git revert da39a70eb
```

---

## 7. io_uring Support (EXPERIMENTAL)

### Purpose
Add checkpoint/restore support for io_uring async I/O.

### Commits
| Commit | Description |
|--------|-------------|
| `6609a6db2` | feat(io_uring): Add checkpoint/restore support for io_uring |
| `7bb695550` | test(zdtm): Add io_uring00 checkpoint/restore test |
| `7f6c3d488` | fix(build): Fix io_uring build issues |
| `7293b0fac` | io_uring: Fix FD handling and VMA inheritance |
| `fa21a8bc5` | io_uring: Fix epoll handling and SQPOLL thread skipping |
| `9e25c21e9` | fix: Exclude io_uring VMAs from VMA_UNSUPP check |
| `f3ddb4f3e` | docs: Update io_uring status with test info |
| `dba2abaa9` | docs: Add BUILD.md with io_uring build instructions |

### Files Modified
- `criu/io_uring.c` (new file)
- `criu/include/io_uring.h` (new file)
- `criu/cr-dump.c`, `criu/cr-restore.c`
- `criu/pie/restorer.c`
- `criu/proc_parse.c`
- `criu/eventpoll.c`
- `criu/seize.c`
- `test/zdtm/static/io_uring00.c` (test)

### Why Needed
Modern Python/vLLM uses io_uring for async I/O. Without this, processes using io_uring can't be checkpointed.

### How to Revert
```bash
git revert 9e25c21e9 fa21a8bc5 7293b0fac 7f6c3d488 7bb695550 6609a6db2 f3ddb4f3e dba2abaa9
```

---

## 8. CUDA Plugin Improvements

### Purpose
Enhance the CUDA plugin for better NVIDIA GPU device handling.

### Commits
| Commit | Description |
|--------|-------------|
| `3c5071234` | cuda_plugin: Dynamic detection of all NVIDIA device majors |
| `2c3194dcd` | cuda_plugin: Major improvements for NVSNAP integration |
| `26947e77c` | cuda_plugin: Fix nvidia-uvm major number (235 not 506) |
| `b502521da` | cuda_plugin: Also skip cuda-checkpoint in PAUSE_DEVICES hook |
| `aaecbe944` | cuda_plugin: Add DUMP_EXT_FILE hook and skip cuda-checkpoint |
| `3b82da7d1` | cuda_plugin: Add HANDLE_DEVICE_VMA and UPDATE_VMA_MAP hooks |
| `f27cd04e7` | cuda_plugin: Treat non-CUDA processes as skip (not error) |

### Files Modified
- `plugins/cuda/cuda_plugin.c`

### Why Needed
- Dynamic detection of NVIDIA device major numbers (varies by system)
- Proper handling of nvidia-uvm device
- Integration with external cuda-checkpoint tool
- Skip non-CUDA processes gracefully

### How to Revert
```bash
git revert f27cd04e7 3c5071234 2c3194dcd 26947e77c b502521da aaecbe944 3b82da7d1
```

---

## 9. PID Namespace Handling

### Purpose
Skip PID namespace init process checks for container migration.

### Commits
| Commit | Description |
|--------|-------------|
| `056611f48` | feat: Add pid namespace init check skip and criu-ns script |

### Files Modified
- `criu/namespaces.c`

### Why Needed
Container processes aren't real PID namespace inits. Skip the check.

### How to Revert
```bash
git revert 056611f48
```

---

## 10. Documentation

### Commits
| Commit | Description |
|--------|-------------|
| `1fd8ae787` | docs: Update BUILD.md with Kubernetes container restore guide |

### Files Modified
- `docs/BUILD.md`

### How to Revert
```bash
git revert 1fd8ae787
```

---

## Quick Reference: Revert All

To completely revert to upstream CRIU:

```bash
cd docker/phase2/criu-src
git checkout upstream/criu-dev
git checkout -b criu-clean  # New clean branch
```

## Minimal Required Changes

For basic vLLM checkpoint/restore on Kubernetes, the **minimum required changes** are:

1. `/dev/shm` ghost file handling (`bd1ea9960`, `1c111178d`)
2. `--skip-mnt-ns` flag (`1274d4425`, `0e237c94c`, `d2f070022`)
3. CUDA plugin improvements (all cuda_plugin commits)

Everything else is optional/experimental.
