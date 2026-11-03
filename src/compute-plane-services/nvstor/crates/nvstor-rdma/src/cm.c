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

/* Small rdma-core ABI bridge. Rust owns every handle and all scheduling;
 * all access to a connection is serialized by its I/O thread. No struct ABI
 * of rdma-core is duplicated in Rust: the only shared layouts are the two
 * plain structs below (br_wr, br_event), declared identically in cm.rs.
 *
 * Data path: one completion queue per connection; replies are posted as a
 * chain of unsignaled RDMA WRITEs followed by one signaled SEND, and Read
 * chunks as a chain of RDMA READs whose last WR is signaled. RC completes a
 * send queue in order, so the signaled completion retires the whole chain.
 * Bulk data is never copied here: WRs reference memory the caller registered
 * on the device's protection domain (br_cm_reg). Only RPC/RDMA headers are
 * copied into the connection's registered send slots. */
#define _POSIX_C_SOURCE 200809L
#include <rdma/rdma_cma.h>
#include <infiniband/verbs.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define RECV_BIT (1ULL << 63)
#define POLL_BATCH 32
#define MAX_DEVICES 64

struct br_wr { uint64_t local; uint32_t lkey; uint32_t len; uint64_t remote; uint32_t rkey; uint32_t reserved; };
struct br_event { uint64_t id; uint32_t kind; uint32_t len; int32_t status; uint32_t reserved; };
enum { BR_RECV = 1, BR_SEND_DONE = 2, BR_READ_DONE = 3, BR_FAILED = 4 };

/* One protection domain per device for the process lifetime. Every
 * connection on a device and every buffer registered for it share the PD,
 * so pooled memory can be the DMA source or target of any connection on
 * that device. librdmacm shares one verbs context per device across ids. */
static pthread_mutex_t pd_lock = PTHREAD_MUTEX_INITIALIZER;
static struct { struct ibv_context *verbs; struct ibv_pd *pd; } pds[MAX_DEVICES];
static struct ibv_pd *device_pd(struct ibv_context *verbs) {
    struct ibv_pd *pd = NULL;
    errno = 0;
    pthread_mutex_lock(&pd_lock);
    for (int i = 0; i < MAX_DEVICES; ++i) {
        if (pds[i].verbs == verbs) { pd = pds[i].pd; break; }
        if (!pds[i].verbs) {
            pd = ibv_alloc_pd(verbs);
            if (pd) { pds[i].verbs = verbs; pds[i].pd = pd; }
            break;
        }
    }
    if (!pd && !errno) errno = ENOSPC;
    pthread_mutex_unlock(&pd_lock);
    return pd;
}

struct br_listener { struct rdma_event_channel *channel; struct rdma_cm_id *id; };
struct br_conn {
    struct rdma_cm_id *id;
    struct rdma_event_channel *channel;
    struct ibv_pd *pd;
    struct ibv_comp_channel *cq_channel;
    struct ibv_cq *cq;
    struct ibv_mr *rx_mr, *tx_mr;
    unsigned char *rx, *tx;
    struct ibv_send_wr *wrs;
    struct ibv_sge *sges;
    uint32_t rx_size, rx_count, tx_count, sq_depth;
    uint8_t peer_depth;
    int accepted, established, failed, armed;
};

static int neg_errno(void) { return -(errno ? errno : EIO); }
static int wait_fd(int fd, int timeout) {
    struct pollfd p = { .fd = fd, .events = POLLIN };
    int rc;
    do { rc = poll(&p, 1, timeout); } while (rc < 0 && errno == EINTR);
    if (rc < 0) return neg_errno();
    if (p.revents & (POLLERR | POLLHUP | POLLNVAL)) return -ECONNRESET;
    return rc;
}
static int nonblocking(int fd) {
    int flags = fcntl(fd, F_GETFL);
    return flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0 ? neg_errno() : 0;
}
static int64_t now_us(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000 + ts.tv_nsec / 1000;
}
static void drain_cq_channel(struct ibv_comp_channel *channel) {
    if (!channel) return;
    while (wait_fd(channel->fd, 0) > 0) {
        struct ibv_cq *cq; void *ctx;
        if (ibv_get_cq_event(channel, &cq, &ctx)) break;
        ibv_ack_cq_events(cq, 1);
    }
}
void br_cm_conn_destroy(struct br_conn *c) {
    if (!c) return;
    if (c->id && c->accepted) rdma_disconnect(c->id);
    if (c->id && !c->accepted) rdma_reject(c->id, NULL, 0);
    /* Destroying the QP quiesces DMA before any buffer it referenced is
     * deregistered or reused, including on timeout and error paths. */
    if (c->id && c->id->qp) rdma_destroy_qp(c->id);
    if (c->tx_mr) ibv_dereg_mr(c->tx_mr);
    if (c->rx_mr) ibv_dereg_mr(c->rx_mr);
    free(c->tx); free(c->rx); free(c->wrs); free(c->sges);
    drain_cq_channel(c->cq_channel);
    if (c->cq) ibv_destroy_cq(c->cq);
    if (c->cq_channel) ibv_destroy_comp_channel(c->cq_channel);
    if (c->channel) {
        while (wait_fd(c->channel->fd, 0) > 0) {
            struct rdma_cm_event *event;
            if (rdma_get_cm_event(c->channel, &event)) break;
            rdma_ack_cm_event(event);
        }
    }
    if (c->id) rdma_destroy_id(c->id);
    if (c->channel) rdma_destroy_event_channel(c->channel);
    free(c);
}
void br_cm_listener_destroy(struct br_listener *l) {
    if (!l) return;
    if (l->channel) {
        while (wait_fd(l->channel->fd, 0) > 0) {
            struct rdma_cm_event *event;
            if (rdma_get_cm_event(l->channel, &event)) break;
            struct rdma_cm_id *id = event->id;
            int request = event->event == RDMA_CM_EVENT_CONNECT_REQUEST;
            rdma_ack_cm_event(event);
            if (request) { rdma_reject(id, NULL, 0); rdma_destroy_id(id); }
        }
    }
    if (l->id) rdma_destroy_id(l->id);
    if (l->channel) rdma_destroy_event_channel(l->channel);
    free(l);
}
int br_cm_listen(const struct sockaddr *addr, struct br_listener **out) {
    struct br_listener *l = calloc(1, sizeof(*l));
    if (!l) return -ENOMEM;
    /* An IPv6 listener also accepts IPv4 peers, whatever the node's
     * net.ipv6.bindv6only says (RDMA-CM's default for AFONLY). */
    int afonly = 0;
    l->channel = rdma_create_event_channel();
    if (!l->channel || rdma_create_id(l->channel, &l->id, NULL, RDMA_PS_TCP)
        || (addr->sa_family == AF_INET6
            && rdma_set_option(l->id, RDMA_OPTION_ID, RDMA_OPTION_ID_AFONLY, &afonly, sizeof(afonly)))
        || rdma_bind_addr(l->id, (struct sockaddr *)addr) || rdma_listen(l->id, 64)) {
        int error = neg_errno(); br_cm_listener_destroy(l); return error;
    }
    *out = l; return 0;
}
int br_cm_next(struct br_listener *l, int timeout, struct br_conn **out,
               struct sockaddr_storage *peer, unsigned char *private_data, uint32_t *private_len) {
    int ready = wait_fd(l->channel->fd, timeout);
    if (ready <= 0) return ready;
    struct rdma_cm_event *event;
    if (rdma_get_cm_event(l->channel, &event)) return neg_errno();
    if (event->event != RDMA_CM_EVENT_CONNECT_REQUEST) {
        int error = event->event == RDMA_CM_EVENT_DEVICE_REMOVAL ? -ENODEV : 0;
        rdma_ack_cm_event(event); return error;
    }
    struct br_conn *c = calloc(1, sizeof(*c));
    if (!c) {
        struct rdma_cm_id *id = event->id;
        rdma_ack_cm_event(event); rdma_reject(id, NULL, 0); rdma_destroy_id(id);
        return -ENOMEM;
    }
    c->id = event->id;
    /* rdma_cm reports CONNECT_REQUEST depths from the listener's side:
     * initiator_depth is the peer's responder_resources, i.e. how many RDMA
     * READs we may have outstanding against it. (responder_resources is the
     * peer's initiator depth, which the Linux NFS client sets to 0.) */
    c->peer_depth = event->param.conn.initiator_depth;
    *private_len = event->param.conn.private_data_len;
    if (*private_len) memcpy(private_data, event->param.conn.private_data, *private_len);
    const struct sockaddr *addr = rdma_get_peer_addr(c->id);
    size_t size = addr->sa_family == AF_INET ? sizeof(struct sockaddr_in) : sizeof(struct sockaddr_in6);
    memset(peer, 0, sizeof(*peer)); memcpy(peer, addr, size);
    rdma_ack_cm_event(event);
    c->channel = rdma_create_event_channel();
    if (!c->channel || rdma_migrate_id(c->id, c->channel)) {
        int error = neg_errno(); br_cm_conn_destroy(c); return error;
    }
    *out = c; return 1;
}
static int post_receive(struct br_conn *c, uint32_t index) {
    struct ibv_sge sge = { .addr = (uintptr_t)(c->rx + (size_t)index * c->rx_size),
        .length = c->rx_size, .lkey = c->rx_mr->lkey };
    struct ibv_recv_wr wr = { .wr_id = RECV_BIT | index, .sg_list = &sge, .num_sge = 1 }, *bad;
    int rc = ibv_post_recv(c->id->qp, &wr, &bad);
    return rc ? -rc : 0;
}
static void *aligned(size_t size) {
    void *p = NULL;
    return posix_memalign(&p, 4096, size ? size : 1) ? NULL : p;
}
/* rx_count receive buffers of rx_size bytes, tx_count send slots of rx_size
 * bytes, sq_depth send-queue entries. *read_depth receives the negotiated
 * number of outstanding RDMA READs (0: the peer allows none). */
int br_cm_accept(struct br_conn *c, uint32_t rx_size, uint32_t rx_count, uint32_t tx_count,
                 uint32_t sq_depth, const void *private_data, uint8_t private_len, uint32_t *read_depth) {
    if (!rx_size || rx_size > 262144 || !rx_count || rx_count > 4096 || !tx_count
        || tx_count > 4096 || sq_depth < 2 || sq_depth > 65536) return -EINVAL;
    struct ibv_device_attr attr;
    int rc = ibv_query_device(c->id->verbs, &attr);
    if (rc) return -rc;
    if ((int)sq_depth > attr.max_qp_wr || (int)rx_count > attr.max_qp_wr
        || (int)(rx_count + sq_depth) > attr.max_cqe) return -EINVAL;
    uint8_t ord = c->peer_depth;
    if (attr.max_qp_init_rd_atom < ord) ord = (uint8_t)attr.max_qp_init_rd_atom;
    c->rx_size = rx_size; c->rx_count = rx_count; c->tx_count = tx_count; c->sq_depth = sq_depth;
    c->pd = device_pd(c->id->verbs);
    c->cq_channel = ibv_create_comp_channel(c->id->verbs);
    if (!c->pd || !c->cq_channel) return neg_errno();
    if ((rc = nonblocking(c->cq_channel->fd)) || (rc = nonblocking(c->channel->fd))) return rc;
    c->cq = ibv_create_cq(c->id->verbs, (int)(rx_count + sq_depth), NULL, c->cq_channel, 0);
    if (!c->cq) return neg_errno();
    struct ibv_qp_init_attr qp = { .send_cq = c->cq, .recv_cq = c->cq, .qp_type = IBV_QPT_RC,
        .cap = { .max_send_wr = sq_depth, .max_recv_wr = rx_count, .max_send_sge = 1, .max_recv_sge = 1 } };
    if (rdma_create_qp(c->id, c->pd, &qp)) return neg_errno();
    c->rx = aligned((size_t)rx_count * rx_size);
    c->tx = aligned((size_t)tx_count * rx_size);
    c->wrs = calloc(sq_depth, sizeof(*c->wrs));
    c->sges = calloc(sq_depth, sizeof(*c->sges));
    if (!c->rx || !c->tx || !c->wrs || !c->sges) return -ENOMEM;
    c->rx_mr = ibv_reg_mr(c->pd, c->rx, (size_t)rx_count * rx_size, IBV_ACCESS_LOCAL_WRITE);
    c->tx_mr = ibv_reg_mr(c->pd, c->tx, (size_t)tx_count * rx_size, IBV_ACCESS_LOCAL_WRITE);
    if (!c->rx_mr || !c->tx_mr) return neg_errno();
    for (uint32_t i = 0; i < rx_count; ++i) {
        if ((rc = post_receive(c, i))) return rc;
    }
    if (ibv_req_notify_cq(c->cq, 0)) return neg_errno();
    c->armed = 1;
    struct rdma_conn_param param = { .private_data = private_data, .private_data_len = private_len,
        .responder_resources = 0, .initiator_depth = ord, .rnr_retry_count = 7 };
    if (rdma_accept(c->id, &param)) return neg_errno();
    c->accepted = 1;
    *read_depth = ord;
    return 0;
}
/* Device identity for per-device pools, stable for the process lifetime. */
uintptr_t br_cm_device(struct br_conn *c) { return (uintptr_t)c->id->verbs; }
/* Register memory on the connection's device PD. The registration outlives
 * the connection; any connection on the same device may use its lkey. */
struct ibv_mr *br_cm_reg(struct br_conn *c, void *addr, size_t len, uint32_t *lkey) {
    struct ibv_pd *pd = c->pd ? c->pd : device_pd(c->id->verbs);
    if (!pd) return NULL;
    struct ibv_mr *mr = ibv_reg_mr(pd, addr, len, IBV_ACCESS_LOCAL_WRITE);
    if (mr) *lkey = mr->lkey;
    return mr;
}
int br_cm_dereg(struct ibv_mr *mr) {
    int rc = ibv_dereg_mr(mr);
    return rc ? -rc : 0;
}
const unsigned char *br_cm_rx(struct br_conn *c, uint32_t index) {
    return index < c->rx_count ? c->rx + (size_t)index * c->rx_size : NULL;
}
int br_cm_repost(struct br_conn *c, uint32_t index) {
    if (!c->accepted || !c->id->qp || c->failed) return -ENOTCONN;
    if (index >= c->rx_count) return -EINVAL;
    int rc = post_receive(c, index);
    if (rc) c->failed = 1;
    return rc;
}
static int post_chain(struct br_conn *c) {
    struct ibv_send_wr *bad;
    int rc = ibv_post_send(c->id->qp, c->wrs, &bad);
    if (rc) { c->failed = 1; return -rc; }
    return 0;
}
static void fill(struct br_conn *c, uint32_t i, enum ibv_wr_opcode op, uint64_t local,
                 uint32_t lkey, uint32_t len, uint64_t remote, uint32_t rkey) {
    c->sges[i] = (struct ibv_sge){ .addr = local, .length = len, .lkey = lkey };
    c->wrs[i] = (struct ibv_send_wr){ .sg_list = &c->sges[i], .num_sge = 1, .opcode = op };
    c->wrs[i].wr.rdma.remote_addr = remote;
    c->wrs[i].wr.rdma.rkey = rkey;
    if (i) c->wrs[i - 1].next = &c->wrs[i];
}
/* RDMA WRITE every entry of `writes` (unsignaled), then SEND `len` bytes of
 * `header` from send slot `slot`, signaled with wr_id `token`. */
int br_cm_post_reply(struct br_conn *c, const struct br_wr *writes, uint32_t n, uint32_t slot,
                     const void *header, uint32_t len, uint64_t token) {
    if (!c->accepted || !c->id->qp || c->failed) return -ENOTCONN;
    if (slot >= c->tx_count || len > c->rx_size || n >= c->sq_depth || (token & RECV_BIT)) return -EINVAL;
    unsigned char *buf = c->tx + (size_t)slot * c->rx_size;
    memcpy(buf, header, len);
    for (uint32_t i = 0; i < n; ++i)
        fill(c, i, IBV_WR_RDMA_WRITE, writes[i].local, writes[i].lkey, writes[i].len,
             writes[i].remote, writes[i].rkey);
    fill(c, n, IBV_WR_SEND, (uintptr_t)buf, c->tx_mr->lkey, len, 0, 0);
    c->wrs[n].send_flags = IBV_SEND_SIGNALED;
    c->wrs[n].wr_id = token;
    c->wrs[n].next = NULL;
    return post_chain(c);
}
/* RDMA READ every entry of `reads`; only the last is signaled (wr_id `token`). */
int br_cm_post_reads(struct br_conn *c, const struct br_wr *reads, uint32_t n, uint64_t token) {
    if (!c->accepted || !c->id->qp || c->failed) return -ENOTCONN;
    if (!n || n > c->sq_depth || (token & RECV_BIT)) return -EINVAL;
    for (uint32_t i = 0; i < n; ++i)
        fill(c, i, IBV_WR_RDMA_READ, reads[i].local, reads[i].lkey, reads[i].len,
             reads[i].remote, reads[i].rkey);
    c->wrs[n - 1].send_flags = IBV_SEND_SIGNALED;
    c->wrs[n - 1].wr_id = token;
    c->wrs[n - 1].next = NULL;
    return post_chain(c);
}
static int cm_event(struct br_conn *c) {
    struct rdma_cm_event *event;
    if (rdma_get_cm_event(c->channel, &event)) return errno == EAGAIN ? 0 : neg_errno();
    int rc = 0;
    switch (event->event) {
    case RDMA_CM_EVENT_ESTABLISHED: c->established = 1; break;
    case RDMA_CM_EVENT_TIMEWAIT_EXIT: break;
    default: rc = -ECONNRESET; c->failed = 1; break;
    }
    rdma_ack_cm_event(event); return rc;
}
static int drain(struct br_conn *c, struct br_event *events, int max) {
    struct ibv_wc wc[POLL_BATCH];
    int n = ibv_poll_cq(c->cq, max < POLL_BATCH ? max : POLL_BATCH, wc);
    if (n < 0) return -EIO;
    for (int i = 0; i < n; ++i) {
        struct br_event *e = &events[i];
        memset(e, 0, sizeof(*e));
        e->id = wc[i].wr_id & ~RECV_BIT;
        if (wc[i].status != IBV_WC_SUCCESS) {
            c->failed = 1;
            e->kind = BR_FAILED; e->status = (int32_t)wc[i].status; e->len = wc[i].vendor_err;
        } else if (wc[i].wr_id & RECV_BIT) {
            if (wc[i].opcode != IBV_WC_RECV || e->id >= c->rx_count) return -EPROTO;
            e->kind = BR_RECV; e->len = wc[i].byte_len;
        } else if (wc[i].opcode == IBV_WC_RDMA_READ) {
            e->kind = BR_READ_DONE;
        } else {
            e->kind = BR_SEND_DONE;
        }
    }
    return n;
}
/* Fill up to `max` events. Busy-polls the CQ for `spin_us` before arming the
 * completion channel and sleeping; returns 0 on timeout or when `wake_fd`
 * becomes readable, a negative errno on connection failure. */
int br_cm_poll(struct br_conn *c, struct br_event *events, int max, int timeout_ms,
               int spin_us, int wake_fd) {
    if (!c->accepted || !c->id->qp || max <= 0) return -ENOTCONN;
    int64_t start = now_us();
    int64_t deadline = start + (int64_t)timeout_ms * 1000;
    int64_t spin_end = start + spin_us;
    for (;;) {
        if (c->failed) return -ECONNRESET;
        /* Let rdma-core process ESTABLISHED before returning an RPC that
         * might immediately post a reply on this QP. */
        int n = c->established ? drain(c, events, max) : 0;
        if (n) return n;
        int64_t now = now_us();
        if (c->established && now < spin_end) continue;
        if (!c->armed) {
            if (ibv_req_notify_cq(c->cq, 0)) return neg_errno();
            c->armed = 1;
            continue; /* close the race with completions before arming */
        }
        if (now >= deadline) return 0;
        struct pollfd fds[3] = {
            { .fd = c->cq_channel->fd, .events = POLLIN },
            { .fd = c->channel->fd, .events = POLLIN },
            { .fd = wake_fd, .events = POLLIN }
        };
        int remaining = (int)((deadline - now + 999) / 1000);
        int rc = poll(fds, wake_fd >= 0 ? 3 : 2, remaining);
        if (rc < 0) { if (errno == EINTR) continue; return neg_errno(); }
        if (!rc) return 0;
        for (int i = 0; i < 3; ++i)
            if (fds[i].revents & (POLLERR | POLLHUP | POLLNVAL)) return -ECONNRESET;
        if (fds[1].revents & POLLIN) { rc = cm_event(c); if (rc) return rc; }
        if (fds[0].revents & POLLIN) {
            struct ibv_cq *cq; void *context;
            if (ibv_get_cq_event(c->cq_channel, &cq, &context) == 0) {
                ibv_ack_cq_events(cq, 1);
                c->armed = 0;
            } else if (errno != EAGAIN) return neg_errno();
        }
        if (wake_fd >= 0 && (fds[2].revents & POLLIN)) {
            uint64_t count;
            ssize_t received = read(wake_fd, &count, sizeof(count));
            if (received < 0 && errno != EAGAIN && errno != EINTR) return neg_errno();
            return 0;
        }
        spin_end = now_us() + spin_us; /* activity: spin again before sleeping */
    }
}
