/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NCCL Interception for Multi-GPU Checkpoint/Restore
 *
 * Hooks ncclCommInitRank() to track NCCL communicators. On SIGUSR1 quiesce,
 * calls cudaDeviceSynchronize() + ncclCommAbort() on all tracked communicators
 * to remove cross-GPU NCCL dependencies before cuda-checkpoint runs.
 *
 * Without this, cuda-checkpoint --action lock deadlocks on multi-GPU workloads
 * because locking one GPU freezes its NCCL ring buffers, causing other GPUs to
 * block on NCCL collectives that need the frozen GPU.
 *
 * Enable: NVSNAP_NCCL_INTERCEPT=1
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include "nvsnap_intercept.h"

/* =========================================================================
 * NCCL type definitions (no nccl.h dependency)
 * ========================================================================= */

#define NCCL_UNIQUE_ID_BYTES 128

typedef void *ncclComm_t;

typedef struct {
	char internal[NCCL_UNIQUE_ID_BYTES];
} ncclUniqueId;

typedef enum {
	ncclSuccess = 0,
	ncclUnhandledCudaError = 1,
	ncclSystemError = 2,
	ncclInternalError = 3,
	ncclInvalidArgument = 4,
	ncclInvalidUsage = 5,
} ncclResult_t;

/* CUDA runtime */
typedef enum {
	cudaSuccess = 0,
} cudaError_t;

/* NCCL data types and reduction ops (opaque — just pass through) */
typedef int ncclDataType_t;
typedef int ncclRedOp_t;

/* CUDA stream (opaque pointer) */
typedef void *cudaStream_t;

/* =========================================================================
 * Function pointer types
 * ========================================================================= */

typedef ncclResult_t (*ncclCommInitRank_fn)(ncclComm_t *comm, int nranks,
					    ncclUniqueId commId, int rank);
typedef ncclResult_t (*ncclCommAbort_fn)(ncclComm_t comm);
typedef ncclResult_t (*ncclCommDestroy_fn)(ncclComm_t comm);
typedef ncclResult_t (*ncclCommFinalize_fn)(ncclComm_t comm);
typedef ncclResult_t (*ncclGetUniqueId_fn)(ncclUniqueId *uniqueId);
typedef cudaError_t (*cudaDeviceSynchronize_fn)(void);

/* Collective operation function pointer types */
typedef ncclResult_t (*ncclAllReduce_fn)(const void *, void *, size_t,
					 ncclDataType_t, ncclRedOp_t,
					 ncclComm_t, cudaStream_t);
typedef ncclResult_t (*ncclBroadcast_fn)(const void *, void *, size_t,
					  ncclDataType_t, int,
					  ncclComm_t, cudaStream_t);
typedef ncclResult_t (*ncclAllGather_fn)(const void *, void *, size_t,
					  ncclDataType_t,
					  ncclComm_t, cudaStream_t);
typedef ncclResult_t (*ncclReduceScatter_fn)(const void *, void *, size_t,
					      ncclDataType_t, ncclRedOp_t,
					      ncclComm_t, cudaStream_t);
typedef ncclResult_t (*ncclSend_fn)(const void *, size_t, ncclDataType_t,
				     int, ncclComm_t, cudaStream_t);
typedef ncclResult_t (*ncclRecv_fn)(void *, size_t, ncclDataType_t,
				     int, ncclComm_t, cudaStream_t);

/* =========================================================================
 * Global state
 * ========================================================================= */

#define NVSNAP_NCCL_MAX_COMMS 64

typedef struct nvsnap_nccl_comm {
	ncclComm_t real_comm;
	int rank;
	int nranks;
	ncclUniqueId unique_id;
	int aborted;
} nvsnap_nccl_comm_t;

static nvsnap_nccl_comm_t g_nccl_comms[NVSNAP_NCCL_MAX_COMMS];
static int g_nccl_ncomms = 0;
static pthread_mutex_t g_nccl_mutex = PTHREAD_MUTEX_INITIALIZER;
static int g_is_nccl_parent = 0;

/* Real function pointers */
static ncclCommInitRank_fn real_ncclCommInitRank = NULL;
static ncclCommAbort_fn real_ncclCommAbort = NULL;
static ncclCommDestroy_fn real_ncclCommDestroy = NULL;
static ncclCommFinalize_fn real_ncclCommFinalize = NULL;
static ncclGetUniqueId_fn real_ncclGetUniqueId = NULL;
static cudaDeviceSynchronize_fn real_cudaDeviceSynchronize = NULL;

/* Real collective function pointers */
static ncclAllReduce_fn real_ncclAllReduce = NULL;
static ncclBroadcast_fn real_ncclBroadcast = NULL;
static ncclAllGather_fn real_ncclAllGather = NULL;
static ncclReduceScatter_fn real_ncclReduceScatter = NULL;
static ncclSend_fn real_ncclSend = NULL;
static ncclRecv_fn real_ncclRecv = NULL;

/* =========================================================================
 * Comm pointer remap table (old checkpoint comm → new restored comm)
 * ========================================================================= */

#define NVSNAP_NCCL_MAX_REMAP 64

static ncclComm_t g_comm_remap_old[NVSNAP_NCCL_MAX_REMAP];
static ncclComm_t g_comm_remap_new[NVSNAP_NCCL_MAX_REMAP];
static int g_comm_remap_count = 0;

static ncclComm_t nvsnap_nccl_remap_comm(ncclComm_t comm)
{
	for (int i = 0; i < g_comm_remap_count; i++) {
		if (g_comm_remap_old[i] == comm)
			return g_comm_remap_new[i];
	}
	return comm; /* no remap found, pass through */
}

/* Library handles */
static void *g_libnccl = NULL;
static void *g_libcudart = NULL;

/* Use dlvsym to get the real dlsym, bypassing our override in zmq_intercept.c */
static void *(*g_real_dlsym)(void *, const char *) = NULL;

static void *nccl_real_dlsym(void *handle, const char *symbol)
{
	if (!g_real_dlsym)
		g_real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
	if (g_real_dlsym)
		return g_real_dlsym(handle, symbol);
	return NULL;
}

/* Init state */
static pthread_once_t g_nccl_once = PTHREAD_ONCE_INIT;
static int g_nccl_enabled = -1; /* -1 = unchecked */

/* =========================================================================
 * Fork handling
 * ========================================================================= */

/*
 * Mark this process as the NCCL parent (called via pthread_atfork prepare).
 * EngineCore creates NCCL comms then forks TP workers. After fork, both
 * parent and children receive quiesce triggers. If the parent calls
 * ncclCommFinalize/Destroy, it corrupts shared kernel-side NCCL state
 * (proxy sockets, FDs) that workers are actively using. Skip quiesce
 * in the parent — children own the comms after fork.
 */
void nvsnap_nccl_mark_parent(void)
{
	if (g_nccl_ncomms > 0)
		g_is_nccl_parent = 1;
}

/*
 * Reset NCCL tracking state after fork. Called from quiesce.c's
 * pthread_atfork child handler. The child process will create its own
 * NCCL communicators — we must not carry stale entries from the parent.
 */
void nvsnap_nccl_atfork_child(void)
{
	g_nccl_once = (pthread_once_t)PTHREAD_ONCE_INIT;
	g_nccl_ncomms = 0;
	g_nccl_mutex = (pthread_mutex_t)PTHREAD_MUTEX_INITIALIZER;
	real_ncclCommInitRank = NULL;
	real_ncclCommAbort = NULL;
	real_ncclCommDestroy = NULL;
	real_ncclCommFinalize = NULL;
	real_ncclGetUniqueId = NULL;
	real_cudaDeviceSynchronize = NULL;
	g_real_dlsym = NULL;
	g_nccl_enabled = -1;
	/* Don't close g_libnccl / g_libcudart — shared with parent, child inherits */
	g_libnccl = NULL;
	g_libcudart = NULL;
}

/* =========================================================================
 * Helpers
 * ========================================================================= */

int nvsnap_nccl_is_enabled(void)
{
	if (g_nccl_enabled < 0) {
		const char *env = getenv("NVSNAP_NCCL_INTERCEPT");
		g_nccl_enabled = (env && strcmp(env, "1") == 0) ? 1 : 0;
	}
	return g_nccl_enabled;
}

static void nvsnap_nccl_load_real(void)
{
	if (!nvsnap_nccl_is_enabled())
		return;

	/* Find libnccl.so — try env var first, then standard names */
	const char *nccl_path = getenv("VLLM_NCCL_SO_PATH");
	if (nccl_path && nccl_path[0]) {
		g_libnccl = dlopen(nccl_path, RTLD_LAZY | RTLD_GLOBAL);
		if (g_libnccl)
			NVSNAP_INFO("Loaded NCCL from VLLM_NCCL_SO_PATH: %s", nccl_path);
	}
	if (!g_libnccl) {
		g_libnccl = dlopen("libnccl.so.2", RTLD_LAZY | RTLD_GLOBAL);
		if (g_libnccl)
			NVSNAP_INFO("Loaded NCCL from libnccl.so.2");
	}
	if (!g_libnccl) {
		/* Try RTLD_NEXT — NCCL may already be loaded by the application */
		real_ncclCommInitRank = (ncclCommInitRank_fn)nccl_real_dlsym(RTLD_NEXT, "ncclCommInitRank");
		if (real_ncclCommInitRank) {
			NVSNAP_INFO("Found ncclCommInitRank via RTLD_NEXT");
			real_ncclCommAbort = (ncclCommAbort_fn)nccl_real_dlsym(RTLD_NEXT, "ncclCommAbort");
			real_ncclCommDestroy = (ncclCommDestroy_fn)nccl_real_dlsym(RTLD_NEXT, "ncclCommDestroy");
			real_ncclCommFinalize = (ncclCommFinalize_fn)nccl_real_dlsym(RTLD_NEXT, "ncclCommFinalize");
			real_ncclGetUniqueId = (ncclGetUniqueId_fn)nccl_real_dlsym(RTLD_NEXT, "ncclGetUniqueId");
		} else {
			NVSNAP_WARN("Could not find libnccl.so — NCCL interception disabled");
			g_nccl_enabled = 0;
			return;
		}
	}

	if (g_libnccl && !real_ncclCommInitRank) {
		real_ncclCommInitRank = (ncclCommInitRank_fn)nccl_real_dlsym(g_libnccl, "ncclCommInitRank");
		real_ncclCommAbort = (ncclCommAbort_fn)nccl_real_dlsym(g_libnccl, "ncclCommAbort");
		real_ncclCommDestroy = (ncclCommDestroy_fn)nccl_real_dlsym(g_libnccl, "ncclCommDestroy");
		real_ncclCommFinalize = (ncclCommFinalize_fn)nccl_real_dlsym(g_libnccl, "ncclCommFinalize");
		real_ncclGetUniqueId = (ncclGetUniqueId_fn)nccl_real_dlsym(g_libnccl, "ncclGetUniqueId");
	}

	/* Load collective functions from wherever we found NCCL */
	{
		void *h = g_libnccl ? g_libnccl : RTLD_NEXT;
		if (!real_ncclAllReduce)
			real_ncclAllReduce = (ncclAllReduce_fn)nccl_real_dlsym(h, "ncclAllReduce");
		if (!real_ncclBroadcast)
			real_ncclBroadcast = (ncclBroadcast_fn)nccl_real_dlsym(h, "ncclBroadcast");
		if (!real_ncclAllGather)
			real_ncclAllGather = (ncclAllGather_fn)nccl_real_dlsym(h, "ncclAllGather");
		if (!real_ncclReduceScatter)
			real_ncclReduceScatter = (ncclReduceScatter_fn)nccl_real_dlsym(h, "ncclReduceScatter");
		if (!real_ncclSend)
			real_ncclSend = (ncclSend_fn)nccl_real_dlsym(h, "ncclSend");
		if (!real_ncclRecv)
			real_ncclRecv = (ncclRecv_fn)nccl_real_dlsym(h, "ncclRecv");
	}

	if (!real_ncclCommInitRank || !real_ncclCommAbort) {
		NVSNAP_WARN("Could not resolve NCCL functions — interception disabled");
		g_nccl_enabled = 0;
		return;
	}

	/* Find cudaDeviceSynchronize */
	g_libcudart = dlopen("libcudart.so", RTLD_LAZY | RTLD_GLOBAL);
	if (!g_libcudart)
		g_libcudart = dlopen("libcudart.so.12", RTLD_LAZY | RTLD_GLOBAL);
	if (g_libcudart) {
		real_cudaDeviceSynchronize = (cudaDeviceSynchronize_fn)nccl_real_dlsym(
			g_libcudart, "cudaDeviceSynchronize");
	}
	if (!real_cudaDeviceSynchronize) {
		real_cudaDeviceSynchronize = (cudaDeviceSynchronize_fn)nccl_real_dlsym(
			RTLD_NEXT, "cudaDeviceSynchronize");
	}
	if (!real_cudaDeviceSynchronize)
		NVSNAP_WARN("Could not find cudaDeviceSynchronize — will skip GPU sync before NCCL abort");

	NVSNAP_INFO("NCCL interception initialized: ncclCommInitRank=%p ncclCommAbort=%p cudaDeviceSync=%p",
		   (void *)real_ncclCommInitRank, (void *)real_ncclCommAbort,
		   (void *)real_cudaDeviceSynchronize);
}

/* =========================================================================
 * Shared memory persistence for comm tracking across fork
 *
 * Parent writes comm entries to /dev/shm/nvsnap-nccl-comms-<pid>.
 * After fork, child's nvsnap_nccl_quiesce() calls nvsnap_nccl_recover_comms()
 * to load the parent's entries. Comm pointers are valid in children
 * because fork() copies the address space.
 * ========================================================================= */

#define NVSNAP_NCCL_SHM_PREFIX "nvsnap-nccl-comms-"

typedef struct {
	uint64_t comm_ptr;
	int rank;
	int nranks;
} nvsnap_nccl_shm_entry_t;

/* Write current comm table to /dev/shm. Caller holds g_nccl_mutex. */
static void nvsnap_nccl_persist_comms_locked(void)
{
	char path[PATH_MAX];
	snprintf(path, sizeof(path), "/dev/shm/%s%d", NVSNAP_NCCL_SHM_PREFIX, getpid());

	FILE *f = fopen(path, "w");
	if (!f) {
		NVSNAP_WARN("Failed to persist NCCL comms to %s: %s", path, strerror(errno));
		return;
	}

	for (int i = 0; i < g_nccl_ncomms; i++) {
		nvsnap_nccl_shm_entry_t entry = {
			.comm_ptr = (uint64_t)g_nccl_comms[i].real_comm,
			.rank = g_nccl_comms[i].rank,
			.nranks = g_nccl_comms[i].nranks,
		};
		fwrite(&entry, sizeof(entry), 1, f);
	}
	fclose(f);
	NVSNAP_DEBUG("Persisted %d NCCL comms to %s", g_nccl_ncomms, path);
}

/* Recover comm table from parent's shared memory file. */
static int nvsnap_nccl_recover_comms(void)
{
	DIR *dir = opendir("/dev/shm");
	if (!dir)
		return 0;

	int recovered = 0;
	struct dirent *ent;
	while ((ent = readdir(dir)) != NULL) {
		if (strncmp(ent->d_name, NVSNAP_NCCL_SHM_PREFIX,
			    strlen(NVSNAP_NCCL_SHM_PREFIX)) != 0)
			continue;

		char path[PATH_MAX];
		snprintf(path, sizeof(path), "/dev/shm/%s", ent->d_name);

		FILE *f = fopen(path, "r");
		if (!f)
			continue;

		nvsnap_nccl_shm_entry_t entry;
		while (fread(&entry, sizeof(entry), 1, f) == 1) {
			if (g_nccl_ncomms >= NVSNAP_NCCL_MAX_COMMS)
				break;

			/* Skip duplicates */
			int dup = 0;
			for (int i = 0; i < g_nccl_ncomms; i++) {
				if ((uint64_t)g_nccl_comms[i].real_comm == entry.comm_ptr) {
					dup = 1;
					break;
				}
			}
			if (dup)
				continue;

			g_nccl_comms[g_nccl_ncomms].real_comm = (ncclComm_t)entry.comm_ptr;
			g_nccl_comms[g_nccl_ncomms].rank = entry.rank;
			g_nccl_comms[g_nccl_ncomms].nranks = entry.nranks;
			g_nccl_comms[g_nccl_ncomms].aborted = 0;
			g_nccl_ncomms++;
			recovered++;
		}
		fclose(f);
	}
	closedir(dir);

	if (recovered > 0)
		NVSNAP_INFO("Recovered %d NCCL comms from shared memory", recovered);

	return recovered;
}

/* ncclCommInitRank hook is now in nvsnap_interpose_nccl.c (NvSnap's version).
 * NvSnap handles allocation tracking + NCCL lifecycle.
 * NvSnap keeps collective hooks (ncclAllReduce, etc.) for restore remapping. */

/* =========================================================================
 * Quiesce: abort all communicators before checkpoint
 * ========================================================================= */

void nvsnap_nccl_quiesce(void)
{
	if (!nvsnap_nccl_is_enabled())
		return;

	pthread_mutex_lock(&g_nccl_mutex);
	int ncomms = g_nccl_ncomms;
	pthread_mutex_unlock(&g_nccl_mutex);

	if (ncomms == 0) {
		NVSNAP_INFO("NCCL quiesce: no communicators tracked, skipping");
		return;
	}

	/* No-op: NCCL destroy is handled by NvSnap's nvsnap_pre_checkpoint_quiesce()
	 * which is called via dlsym from the quiesce path. NvSnap only tracks comms
	 * for observability — the actual destroy must be done by the library that
	 * also tracks allocations (NvSnap), because NCCL destroy order matters
	 * relative to P2P disable and D2H save.
	 *
	 * The working 322 GB checkpoint (v0.9.97) used this exact pattern:
	 * nvsnap_nccl_quiesce = no-op, NvSnap does the real work. */
	NVSNAP_INFO("NCCL quiesce: %d communicator(s) tracked (handled by NvSnap)", ncomms);
}

/* =========================================================================
 * Restore: recreate NCCL communicators after CRIU restore
 *
 * All ranks coordinate via /dev/shm:
 *   - Rank 0 generates a new ncclUniqueId, writes it to /dev/shm/nvsnap-nccl-uid
 *   - Other ranks poll for /dev/shm/nvsnap-nccl-uid-ready, then read the ID
 *   - All ranks call ncclCommInitRank() with the new ID
 *   - Old comm pointers are mapped to new ones in the remap table
 * ========================================================================= */

#define NVSNAP_NCCL_UID_SHM      "/dev/shm/nvsnap-nccl-uid"
#define NVSNAP_NCCL_UID_READY_SHM "/dev/shm/nvsnap-nccl-uid-ready"
#define NVSNAP_NCCL_RESTORE_POLL_US 10000   /* 10ms */
#define NVSNAP_NCCL_RESTORE_TIMEOUT_S 60

void nvsnap_nccl_restore(void)
{
	if (!nvsnap_nccl_is_enabled())
		return;

	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);

	pthread_mutex_lock(&g_nccl_mutex);
	int ncomms = g_nccl_ncomms;
	pthread_mutex_unlock(&g_nccl_mutex);

	if (ncomms == 0)
		return;

	if (!real_ncclCommInitRank || !real_ncclGetUniqueId) {
		NVSNAP_ERROR("NCCL restore: missing ncclCommInitRank or ncclGetUniqueId");
		return;
	}

	NVSNAP_INFO("NCCL restore: recreating %d communicator(s)", ncomms);

	/*
	 * Recreate each communicator group. Multiple comm groups (TP, PP, DP)
	 * are tracked separately in g_nccl_comms[]. We recreate them in order,
	 * using per-group shm files to coordinate unique IDs across ranks.
	 */
	pthread_mutex_lock(&g_nccl_mutex);
	for (int i = 0; i < g_nccl_ncomms; i++) {
		nvsnap_nccl_comm_t *entry = &g_nccl_comms[i];
		ncclUniqueId new_id;
		char uid_path[256];
		char ready_path[256];

		/* Per-comm-group shm paths to allow multiple groups */
		snprintf(uid_path, sizeof(uid_path),
			 NVSNAP_NCCL_UID_SHM "-%d", i);
		snprintf(ready_path, sizeof(ready_path),
			 NVSNAP_NCCL_UID_READY_SHM "-%d", i);

		if (entry->rank == 0) {
			/* Rank 0: generate new unique ID and share via shm */
			ncclResult_t rc = real_ncclGetUniqueId(&new_id);
			if (rc != ncclSuccess) {
				NVSNAP_ERROR("NCCL restore: ncclGetUniqueId failed rc=%d", (int)rc);
				pthread_mutex_unlock(&g_nccl_mutex);
				return;
			}

			/* Write ID to shm */
			int fd = open(uid_path, O_CREAT | O_WRONLY | O_TRUNC, 0666);
			if (fd < 0) {
				NVSNAP_ERROR("NCCL restore: cannot create %s: %s",
					   uid_path, strerror(errno));
				pthread_mutex_unlock(&g_nccl_mutex);
				return;
			}
			ssize_t n = write(fd, &new_id, sizeof(new_id));
			close(fd);
			if (n != sizeof(new_id)) {
				NVSNAP_ERROR("NCCL restore: short write to %s", uid_path);
				pthread_mutex_unlock(&g_nccl_mutex);
				return;
			}

			/* Signal ready */
			fd = open(ready_path, O_CREAT | O_WRONLY, 0666);
			if (fd >= 0)
				close(fd);

			NVSNAP_INFO("NCCL restore: rank 0 wrote unique ID to %s", uid_path);
		} else {
			/* Other ranks: poll for ready marker */
			int waited = 0;
			while (access(ready_path, F_OK) != 0) {
				usleep(NVSNAP_NCCL_RESTORE_POLL_US);
				waited++;
				if (waited * NVSNAP_NCCL_RESTORE_POLL_US >
				    NVSNAP_NCCL_RESTORE_TIMEOUT_S * 1000000) {
					NVSNAP_ERROR("NCCL restore: timeout waiting for %s",
						   ready_path);
					pthread_mutex_unlock(&g_nccl_mutex);
					return;
				}
			}

			/* Read unique ID */
			int fd = open(uid_path, O_RDONLY);
			if (fd < 0) {
				NVSNAP_ERROR("NCCL restore: cannot open %s: %s",
					   uid_path, strerror(errno));
				pthread_mutex_unlock(&g_nccl_mutex);
				return;
			}
			ssize_t n = read(fd, &new_id, sizeof(new_id));
			close(fd);
			if (n != sizeof(new_id)) {
				NVSNAP_ERROR("NCCL restore: short read from %s", uid_path);
				pthread_mutex_unlock(&g_nccl_mutex);
				return;
			}

			NVSNAP_INFO("NCCL restore: rank %d read unique ID from %s",
				   entry->rank, uid_path);
		}

		/* All ranks: create new communicator */
		ncclComm_t new_comm = NULL;
		NVSNAP_INFO("NCCL restore: ncclCommInitRank(nranks=%d, rank=%d) for comm %d",
			   entry->nranks, entry->rank, i);

		ncclResult_t rc = real_ncclCommInitRank(&new_comm, entry->nranks,
							new_id, entry->rank);
		if (rc != ncclSuccess) {
			NVSNAP_ERROR("NCCL restore: ncclCommInitRank failed rc=%d for comm %d",
				   (int)rc, i);
			pthread_mutex_unlock(&g_nccl_mutex);
			return;
		}

		NVSNAP_INFO("NCCL restore: comm %d recreated: old=%p new=%p rank=%d/%d",
			   i, (void *)entry->real_comm, (void *)new_comm,
			   entry->rank, entry->nranks);

		/* Store remap: old_comm → new_comm */
		if (g_comm_remap_count < NVSNAP_NCCL_MAX_REMAP) {
			g_comm_remap_old[g_comm_remap_count] = entry->real_comm;
			g_comm_remap_new[g_comm_remap_count] = new_comm;
			g_comm_remap_count++;
		}

		/* Update tracking entry */
		entry->real_comm = new_comm;
		entry->aborted = 0;
		memcpy(&entry->unique_id, &new_id, sizeof(ncclUniqueId));

		/* Cleanup shm (rank 0 only, after all ranks have read) */
		if (entry->rank == 0) {
			/* Small delay to ensure other ranks have read the file */
			usleep(100000); /* 100ms */
			unlink(uid_path);
			unlink(ready_path);
		}
	}
	pthread_mutex_unlock(&g_nccl_mutex);

	NVSNAP_INFO("NCCL restore: done (%d communicators recreated, %d remaps)",
		   ncomms, g_comm_remap_count);
}

/* =========================================================================
 * Collective hooks: transparently remap old comm pointers to new ones
 *
 * After restore, PyTorch/vLLM hold stale ncclComm_t pointers from before
 * checkpoint. These hooks intercept collective calls, look up the old
 * pointer in the remap table, and substitute the new communicator.
 * ========================================================================= */

ncclResult_t ncclAllReduce(const void *sendbuff, void *recvbuff, size_t count,
			    ncclDataType_t datatype, ncclRedOp_t op,
			    ncclComm_t comm, cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclAllReduce) {
		real_ncclAllReduce = (ncclAllReduce_fn)nccl_real_dlsym(RTLD_NEXT, "ncclAllReduce");
		if (!real_ncclAllReduce) return ncclInternalError;
	}
	return real_ncclAllReduce(sendbuff, recvbuff, count, datatype, op,
				   nvsnap_nccl_remap_comm(comm), stream);
}

ncclResult_t ncclBroadcast(const void *sendbuff, void *recvbuff, size_t count,
			    ncclDataType_t datatype, int root,
			    ncclComm_t comm, cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclBroadcast) {
		real_ncclBroadcast = (ncclBroadcast_fn)nccl_real_dlsym(RTLD_NEXT, "ncclBroadcast");
		if (!real_ncclBroadcast) return ncclInternalError;
	}
	return real_ncclBroadcast(sendbuff, recvbuff, count, datatype, root,
				   nvsnap_nccl_remap_comm(comm), stream);
}

ncclResult_t ncclAllGather(const void *sendbuff, void *recvbuff, size_t sendcount,
			    ncclDataType_t datatype,
			    ncclComm_t comm, cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclAllGather) {
		real_ncclAllGather = (ncclAllGather_fn)nccl_real_dlsym(RTLD_NEXT, "ncclAllGather");
		if (!real_ncclAllGather) return ncclInternalError;
	}
	return real_ncclAllGather(sendbuff, recvbuff, sendcount, datatype,
				   nvsnap_nccl_remap_comm(comm), stream);
}

ncclResult_t ncclReduceScatter(const void *sendbuff, void *recvbuff,
				size_t recvcount, ncclDataType_t datatype,
				ncclRedOp_t op, ncclComm_t comm,
				cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclReduceScatter) {
		real_ncclReduceScatter = (ncclReduceScatter_fn)nccl_real_dlsym(RTLD_NEXT, "ncclReduceScatter");
		if (!real_ncclReduceScatter) return ncclInternalError;
	}
	return real_ncclReduceScatter(sendbuff, recvbuff, recvcount, datatype,
				       op, nvsnap_nccl_remap_comm(comm), stream);
}

ncclResult_t ncclSend(const void *sendbuff, size_t count,
		       ncclDataType_t datatype, int peer,
		       ncclComm_t comm, cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclSend) {
		real_ncclSend = (ncclSend_fn)nccl_real_dlsym(RTLD_NEXT, "ncclSend");
		if (!real_ncclSend) return ncclInternalError;
	}
	return real_ncclSend(sendbuff, count, datatype, peer,
			      nvsnap_nccl_remap_comm(comm), stream);
}

ncclResult_t ncclRecv(void *recvbuff, size_t count,
		       ncclDataType_t datatype, int peer,
		       ncclComm_t comm, cudaStream_t stream)
{
	pthread_once(&g_nccl_once, nvsnap_nccl_load_real);
	if (!real_ncclRecv) {
		real_ncclRecv = (ncclRecv_fn)nccl_real_dlsym(RTLD_NEXT, "ncclRecv");
		if (!real_ncclRecv) return ncclInternalError;
	}
	return real_ncclRecv(recvbuff, count, datatype, peer,
			      nvsnap_nccl_remap_comm(comm), stream);
}

/* =========================================================================
 * dlsym override: redirect NCCL symbol lookups to our hooks
 *
 * Called from the dlsym override in zmq_intercept.c.
 * Returns our wrapper function if the symbol matches, NULL otherwise.
 * ========================================================================= */

void *nvsnap_nccl_symbol_override(const char *symbol)
{
	if (!nvsnap_nccl_is_enabled())
		return NULL;
	/* ncclCommInitRank is handled by NvSnap's symbol table */
	if (strcmp(symbol, "ncclAllReduce") == 0)
		return (void *)ncclAllReduce;
	if (strcmp(symbol, "ncclBroadcast") == 0)
		return (void *)ncclBroadcast;
	if (strcmp(symbol, "ncclAllGather") == 0)
		return (void *)ncclAllGather;
	if (strcmp(symbol, "ncclReduceScatter") == 0)
		return (void *)ncclReduceScatter;
	if (strcmp(symbol, "ncclSend") == 0)
		return (void *)ncclSend;
	if (strcmp(symbol, "ncclRecv") == 0)
		return (void *)ncclRecv;
	return NULL;
}
