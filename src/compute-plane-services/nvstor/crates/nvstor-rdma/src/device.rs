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

//! RDMA device management — context, protection domain, memory registration.

use std::sync::Arc;

use tracing::{debug, info, warn};

use crate::ffi;

/// What remote peers may do to a memory region, or through a queue pair.
///
/// Every registration and every QP states the access it needs instead of
/// inheriting "everything": a region registered with REMOTE_READ is
/// readable by anyone who learns its rkey on a QP that also allows remote
/// reads. Server pools that only push data out (GET) or pull it in
/// (server-pull PUT) need [`Access::LOCAL`]; a client's GET targets need
/// [`Access::REMOTE_WRITE`]; a client's PUT sources need
/// [`Access::REMOTE_READ`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Access(i32);

impl Access {
    /// Local reads and writes only (LOCAL_WRITE is what lets the NIC
    /// DMA into the region, e.g. as the target of our own RDMA READ).
    pub const LOCAL: Self = Self(ffi::IBV_ACCESS_LOCAL_WRITE);
    /// Peers may RDMA-WRITE into the region (GET destinations).
    pub const REMOTE_WRITE: Self = Self(ffi::IBV_ACCESS_LOCAL_WRITE | ffi::IBV_ACCESS_REMOTE_WRITE);
    /// Peers may RDMA-READ from the region (server-pull PUT sources).
    pub const REMOTE_READ: Self = Self(ffi::IBV_ACCESS_LOCAL_WRITE | ffi::IBV_ACCESS_REMOTE_READ);
    /// Both: a client that GETs and PUTs through one pool.
    pub const REMOTE_READ_WRITE: Self = Self(
        ffi::IBV_ACCESS_LOCAL_WRITE | ffi::IBV_ACCESS_REMOTE_WRITE | ffi::IBV_ACCESS_REMOTE_READ,
    );

    /// The `ibv_access_flags` bits for `ibv_reg_mr`.
    pub fn bits(self) -> i32 {
        self.0
    }

    /// Only the remote bits, as a QP's `qp_access_flags`.
    pub fn remote_bits(self) -> u32 {
        (self.0 & (ffi::IBV_ACCESS_REMOTE_WRITE | ffi::IBV_ACCESS_REMOTE_READ)) as u32
    }

    pub fn remote_read(self) -> bool {
        self.0 & ffi::IBV_ACCESS_REMOTE_READ != 0
    }

    pub fn remote_write(self) -> bool {
        self.0 & ffi::IBV_ACCESS_REMOTE_WRITE != 0
    }
}

/// Safe wrapper around ibverbs device context.
pub struct RdmaDevice {
    /// ibverbs context.
    context: *mut ffi::ibv_context,
    /// Protection domain.
    pd: *mut ffi::ibv_pd,
    /// Device name.
    pub name: String,
    /// Device attributes.
    pub attrs: ffi::ibv_device_attr,
    /// Port number (1-based).
    pub port: u8,
}

// SAFETY: ibverbs context is thread-safe.
unsafe impl Send for RdmaDevice {}
unsafe impl Sync for RdmaDevice {}

impl RdmaDevice {
    /// Open the first available RDMA device.
    pub fn open_default() -> Result<Self, String> {
        Self::open_by_index(0, 1)
    }

    /// Open the first RDMA device whose port 1 is in `ACTIVE` state.
    ///
    /// On boxes with multiple HCAs (mlx5_0 + mlx5_1 are common), the
    /// kernel may enumerate a card whose link is down before the one
    /// you actually want — `open_default` would land on the dead card,
    /// every QP transition would fail with `IBV_QPS_RTR -> 22 (EINVAL)`,
    /// and the whole RDMA path would silently fall back to TCP.
    /// `open_first_active` walks the device list and returns the first
    /// HCA whose port carries link.
    pub fn open_first_active() -> Result<Self, String> {
        unsafe {
            let mut num_devices: i32 = 0;
            let dev_list = ffi::ibv_get_device_list(&mut num_devices);
            if dev_list.is_null() || num_devices == 0 {
                return Err("no RDMA devices found".into());
            }
            ffi::ibv_free_device_list(dev_list);
        }
        let mut last_err = "no RDMA devices found".to_string();
        for idx in 0..Self::device_count() {
            let dev = match Self::open_by_index(idx, 1) {
                Ok(d) => d,
                Err(e) => {
                    tracing::debug!(index = idx, error = %e, "RDMA device skipped (open failed)");
                    last_err = e;
                    continue;
                }
            };
            match dev.port_state() {
                Ok(s) if s == "ACTIVE" => return Ok(dev),
                Ok(s) => {
                    tracing::debug!(device = %dev.name, port = dev.port, state = %s,
                        "RDMA device skipped (port not ACTIVE)");
                }
                Err(e) => {
                    tracing::debug!(device = %dev.name, error = %e,
                        "RDMA device skipped (port query failed)");
                }
            }
        }
        Err(format!("no ACTIVE RDMA port found ({last_err})"))
    }

    /// Enumerate all RDMA devices and return their names.
    pub fn list_devices() -> Result<Vec<String>, String> {
        unsafe {
            let mut num_devices: i32 = 0;
            let dev_list = ffi::ibv_get_device_list(&mut num_devices);
            if dev_list.is_null() || num_devices == 0 {
                return Ok(Vec::new());
            }

            let mut names = Vec::new();
            for i in 0..num_devices as usize {
                let device = *dev_list.add(i);
                let name = std::ffi::CStr::from_ptr(ffi::ibv_get_device_name(device))
                    .to_string_lossy()
                    .to_string();
                names.push(name);
            }

            ffi::ibv_free_device_list(dev_list);
            Ok(names)
        }
    }

    /// Number of RDMA devices the verbs library lists (0 when there are
    /// none or the list cannot be read).
    pub fn device_count() -> usize {
        unsafe {
            let mut num_devices: i32 = 0;
            let dev_list = ffi::ibv_get_device_list(&mut num_devices);
            if dev_list.is_null() {
                return 0;
            }
            ffi::ibv_free_device_list(dev_list);
            usize::try_from(num_devices).unwrap_or(0)
        }
    }

    /// Open all available RDMA devices with active ports.
    /// Returns devices that have at least one active port.
    ///
    /// A device that fails to open (a VF in an error state, a uverbs file
    /// without permission, PD exhaustion) is logged and skipped; it does
    /// not hide the devices listed after it.
    pub fn open_all() -> Vec<Self> {
        let mut devices = Vec::new();
        for idx in 0..Self::device_count() {
            match Self::open_by_index(idx, 1) {
                Ok(dev) => {
                    if dev.port_state().map(|s| s == "ACTIVE").unwrap_or(false) {
                        devices.push(dev);
                    }
                }
                Err(e) => warn!(index = idx, error = %e, "RDMA device skipped: open failed"),
            }
        }
        devices
    }

    /// Kernel netdev behind GID `index` (RoCE), read from sysfs. `None`
    /// on InfiniBand ports, which have no netdev.
    pub fn gid_netdev(&self, index: u8) -> Option<String> {
        let path = format!(
            "/sys/class/infiniband/{}/ports/{}/gid_attrs/ndevs/{index}",
            self.name, self.port
        );
        let name = std::fs::read_to_string(path).ok()?;
        let name = name.trim();
        (!name.is_empty()).then(|| name.to_string())
    }

    /// Open a specific RDMA device by index and port.
    pub fn open_by_index(dev_index: usize, port: u8) -> Result<Self, String> {
        unsafe {
            // Get device list.
            let mut num_devices: i32 = 0;
            let dev_list = ffi::ibv_get_device_list(&mut num_devices);
            if dev_list.is_null() || num_devices == 0 {
                return Err("no RDMA devices found".into());
            }

            if dev_index >= num_devices as usize {
                ffi::ibv_free_device_list(dev_list);
                return Err(format!(
                    "device index {} out of range (have {})",
                    dev_index, num_devices
                ));
            }

            let device = *dev_list.add(dev_index);
            let name = std::ffi::CStr::from_ptr(ffi::ibv_get_device_name(device))
                .to_string_lossy()
                .to_string();

            // Open context.
            let context = ffi::ibv_open_device(device);
            ffi::ibv_free_device_list(dev_list);

            if context.is_null() {
                return Err(format!("failed to open device {name}"));
            }

            // Query device attributes.
            let mut attrs: ffi::ibv_device_attr = ffi::zeroed();
            let ret = ffi::ibv_query_device(context, &mut attrs);
            if ret != 0 {
                ffi::ibv_close_device(context);
                return Err(format!("failed to query device {name}: {ret}"));
            }

            // Allocate protection domain.
            let pd = ffi::ibv_alloc_pd(context);
            if pd.is_null() {
                ffi::ibv_close_device(context);
                return Err(format!("failed to alloc PD on {name}"));
            }

            info!(
                device = %name,
                port,
                max_qp = attrs.max_qp,
                max_mr = attrs.max_mr,
                max_cq = attrs.max_cq,
                "RDMA device opened"
            );

            Ok(Self {
                context,
                pd,
                name,
                attrs,
                port,
            })
        }
    }

    /// Get the ibverbs context pointer.
    pub fn context(&self) -> *mut ffi::ibv_context {
        self.context
    }

    /// Completion vectors (interrupt lines) of this device; at least 1.
    pub fn num_comp_vectors(&self) -> u32 {
        // SAFETY: `context` is the live context this device opened; the
        // field is a plain int libibverbs set when it opened it.
        let n = unsafe { (*self.context).num_comp_vectors };
        u32::try_from(n).unwrap_or(0).max(1)
    }

    /// Get the protection domain pointer.
    pub fn pd(&self) -> *mut ffi::ibv_pd {
        self.pd
    }

    /// Register a memory region with exactly the `access` its user needs.
    ///
    /// The region keeps the device (its PD and context) alive, so it is
    /// deregistered safely however long it outlives the caller's handle.
    pub fn register_memory(
        self: &Arc<Self>,
        addr: *mut u8,
        len: usize,
        access: Access,
    ) -> Result<MemoryRegion, String> {
        unsafe {
            let mr = ffi::ibv_reg_mr(self.pd, addr as *mut _, len, access.bits());
            if mr.is_null() {
                return Err("failed to register memory region".into());
            }

            let lkey = (*mr).lkey;
            let rkey = (*mr).rkey;

            debug!(addr = ?addr, len, lkey, rkey, ?access, "registered memory region");

            Ok(MemoryRegion {
                mr,
                lkey,
                rkey,
                _device: Arc::clone(self),
            })
        }
    }

    /// Register `len` bytes of a dma-buf from `offset` into it (GPU memory
    /// exported with `cuMemGetHandleForAddressRange`), so the NIC reads and
    /// writes that memory directly (GPUDirect RDMA). `iova` is the address
    /// peers use for the region: a GET names `iova + n` to land at byte `n`.
    /// The caller keeps `fd` open as long as it likes; the region holds its
    /// own reference to the dma-buf until it is dropped.
    pub fn register_dmabuf(
        self: &Arc<Self>,
        fd: std::os::fd::BorrowedFd<'_>,
        offset: u64,
        len: usize,
        iova: u64,
        access: Access,
    ) -> Result<MemoryRegion, String> {
        use std::os::fd::AsRawFd;
        if len == 0 {
            return Err("dma-buf region of 0 bytes".into());
        }
        let reg = ffi::ibv_reg_dmabuf_mr().ok_or_else(|| {
            format!(
                "{}: this libibverbs cannot register dma-bufs (rdma-core 34 or later needed)",
                self.name
            )
        })?;
        // SAFETY: the PD is live while `self` is; the kernel validates the
        // fd, the range and the access and only returns a null MR on error.
        let mr = unsafe { reg(self.pd, offset, len, iova, fd.as_raw_fd(), access.bits()) };
        if mr.is_null() {
            let e = std::io::Error::last_os_error();
            // ENOTSUPP (524) is kernel-internal, so libc has no text for it.
            let why = if e.raw_os_error() == Some(524) {
                "not supported by this NIC driver for this memory (ENOTSUPP)".to_string()
            } else {
                e.to_string()
            };
            return Err(format!(
                "{}: registering {len} bytes of dma-buf at offset {offset} failed: {why}",
                self.name
            ));
        }
        // SAFETY: a non-null MR returned by ibv_reg_dmabuf_mr is valid until deregistered.
        let (lkey, rkey) = unsafe { ((*mr).lkey, (*mr).rkey) };
        debug!(device = %self.name, iova, len, lkey, rkey, ?access, "registered dma-buf region");
        Ok(MemoryRegion {
            mr,
            lkey,
            rkey,
            _device: Arc::clone(self),
        })
    }

    /// RDMA READs one QP may have outstanding as the initiator, and may
    /// serve at once as the responder, each capped at 16. Server-pull PUT
    /// makes the server an RDMA READ initiator, so neither may be 0.
    pub fn rd_atomic_limits(&self) -> (u8, u8) {
        let clamp = |v: i32| u8::try_from(v.clamp(1, 16)).unwrap_or(1);
        (
            clamp(self.attrs.max_qp_init_rd_atom),
            clamp(self.attrs.max_qp_rd_atom),
        )
    }

    /// Query port state.
    pub fn port_state(&self) -> Result<String, String> {
        unsafe {
            let mut port_attr: ffi::ibv_port_attr = ffi::zeroed();
            let ret = ffi::ibv_query_port(self.context, self.port, &mut port_attr);
            if ret != 0 {
                return Err(format!("query port failed: {ret}"));
            }
            let state = match port_attr.state {
                ffi::IBV_PORT_DOWN => "DOWN",
                ffi::IBV_PORT_INIT => "INIT",
                ffi::IBV_PORT_ARMED => "ARMED",
                ffi::IBV_PORT_ACTIVE => "ACTIVE",
                _ => "UNKNOWN",
            };
            Ok(state.to_string())
        }
    }

    /// Get the LID (Local ID) for this port.
    pub fn lid(&self) -> Result<u16, String> {
        unsafe {
            let mut port_attr: ffi::ibv_port_attr = ffi::zeroed();
            let ret = ffi::ibv_query_port(self.context, self.port, &mut port_attr);
            if ret != 0 {
                return Err(format!("query port failed: {ret}"));
            }
            Ok(port_attr.lid)
        }
    }

    /// Get the active path MTU for this port.
    pub fn active_mtu(&self) -> Result<u32, String> {
        unsafe {
            let mut port_attr: ffi::ibv_port_attr = ffi::zeroed();
            let ret = ffi::ibv_query_port(self.context, self.port, &mut port_attr);
            if ret != 0 {
                return Err(format!("query port failed: {ret}"));
            }
            Ok(port_attr.active_mtu)
        }
    }

    /// Largest message one work request may carry on this port
    /// (`max_msg_sz`; 1 GiB on mlx5, 2 GiB on rxe).
    pub fn max_msg_size(&self) -> Result<u32, String> {
        unsafe {
            let mut port_attr: ffi::ibv_port_attr = ffi::zeroed();
            let ret = ffi::ibv_query_port(self.context, self.port, &mut port_attr);
            if ret != 0 {
                return Err(format!("query port failed: {ret}"));
            }
            Ok(port_attr.max_msg_sz)
        }
    }

    /// Find the best GID index for RoCE v2 (routable IP-based GID).
    ///
    /// GID table layouts differ by driver: mlx5 typically puts the
    /// IPv4-mapped RoCE v2 GID at index 3, Soft-RoCE (rxe) at index 1, and
    /// every NIC reorders as addresses come and go. So candidates are
    /// ranked by what they are, never by position:
    ///
    ///   1. RoCE v2, IPv4-mapped (`::ffff:a.b.c.d`) — routable, and what
    ///      `crate::rail` matches subnets against;
    ///   2. RoCE v2, other non-link-local (global IPv6);
    ///   3. any other non-zero, non-link-local GID (InfiniBand, or no
    ///      sysfs type information);
    ///
    /// lowest index first within a rank. Link-local (`fe80::`) GIDs are
    /// never chosen while anything else exists: a RoCE v2 QP addressed by
    /// one cannot resolve a route and RTR fails with ETIMEDOUT. Falls
    /// back to 0, which is right for InfiniBand (subnet-prefix GID).
    pub fn roce_gid_index(&self) -> u8 {
        let len = self.gid_table_len().clamp(1, 256);
        (0..len)
            .filter_map(|idx| {
                let raw = unsafe { self.gid(idx).ok()?.raw };
                let gid_type = self.gid_type(idx as u8);
                gid_rank(&raw, gid_type.as_deref()).map(|rank| (rank, idx))
            })
            .min()
            .map(|(_, idx)| idx as u8)
            .unwrap_or(0)
    }

    /// Every non-zero entry of this port's GID table, in index order.
    pub fn gid_table(&self) -> Vec<[u8; 16]> {
        (0..self.gid_table_len().clamp(1, 256))
            // SAFETY: every ibv_gid variant is 16 plain bytes.
            .filter_map(|idx| self.gid(idx).ok().map(|g| unsafe { g.raw }))
            .filter(|g| g.iter().any(|&b| b != 0))
            .collect()
    }

    /// Number of GID table entries on this port.
    pub(crate) fn gid_table_len(&self) -> i32 {
        unsafe {
            let mut port_attr: ffi::ibv_port_attr = ffi::zeroed();
            if ffi::ibv_query_port(self.context, self.port, &mut port_attr) != 0 {
                return 16;
            }
            port_attr.gid_tbl_len
        }
    }

    /// GID type ("RoCE v2", "IB/RoCE v1") from sysfs, if exposed.
    pub fn gid_type(&self, index: u8) -> Option<String> {
        let path = format!(
            "/sys/class/infiniband/{}/ports/{}/gid_attrs/types/{index}",
            self.name, self.port
        );
        std::fs::read_to_string(path)
            .ok()
            .map(|t| t.trim().to_string())
    }

    /// Get the GID for RoCE connections.
    pub fn gid(&self, index: i32) -> Result<ffi::ibv_gid, String> {
        unsafe {
            let mut gid: ffi::ibv_gid = ffi::zeroed();
            let ret = ffi::ibv_query_gid(self.context, self.port, index, &mut gid);
            if ret != 0 {
                return Err(format!("query GID failed: {ret}"));
            }
            Ok(gid)
        }
    }
}

/// Whether a RoCE v2 QP can be addressed by this GID across a routed
/// fabric: not all-zero and not link-local. A port that has only such
/// GIDs has not finished address configuration (e.g. SLAAC).
pub fn gid_is_routable(raw: &[u8; 16]) -> bool {
    gid_rank(raw, None).is_some()
}

/// Preference rank of a GID for QP addressing; lower is better, `None`
/// = unusable while anything else exists (zero or link-local). See
/// `RdmaDevice::roce_gid_index`.
fn gid_rank(raw: &[u8; 16], gid_type: Option<&str>) -> Option<u8> {
    if raw.iter().all(|&b| b == 0) || (raw[0] == 0xfe && raw[1] & 0xc0 == 0x80) {
        return None;
    }
    let v2 = gid_type == Some("RoCE v2");
    let ipv4 = crate::rail::gid_ipv4(raw).is_some();
    Some(match (v2, ipv4) {
        (true, true) => 0,
        (true, false) => 1,
        _ => 2,
    })
}

impl Drop for RdmaDevice {
    fn drop(&mut self) {
        unsafe {
            if !self.pd.is_null() {
                ffi::ibv_dealloc_pd(self.pd);
            }
            if !self.context.is_null() {
                ffi::ibv_close_device(self.context);
            }
        }
    }
}

/// Registered memory region for RDMA operations.
///
/// Holds the device it was registered on: `ibv_dereg_mr` reads the MR's
/// context, so the PD and context must outlive the registration. `Drop`
/// deregisters before the fields (and with them the device) are dropped.
pub struct MemoryRegion {
    mr: *mut ffi::ibv_mr,
    pub lkey: u32,
    pub rkey: u32,
    _device: Arc<RdmaDevice>,
}

unsafe impl Send for MemoryRegion {}
unsafe impl Sync for MemoryRegion {}

impl Drop for MemoryRegion {
    fn drop(&mut self) {
        unsafe {
            if !self.mr.is_null() {
                // A failure leaves the region registered: the NIC can still
                // reach memory its owner is about to release. Nothing can be
                // done here but say so.
                let r = ffi::ibv_dereg_mr(self.mr);
                if r != 0 {
                    tracing::error!(device = %self._device.name, errno = r, "ibv_dereg_mr failed: the memory stays registered");
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn open_device() {
        let dev =
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device");
        println!("RDMA device: {}", dev.name);
        println!("Port state: {:?}", dev.port_state());
        println!("Max QP: {}", dev.attrs.max_qp);
        assert!(!dev.name.is_empty());
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device"]
    fn register_dmabuf_refuses_what_is_not_a_dmabuf() {
        use std::os::fd::AsFd;
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let f = std::fs::File::open("/dev/null").expect("open /dev/null");
        let e = dev
            .register_dmabuf(f.as_fd(), 0, 1 << 20, 0x1000_0000, Access::REMOTE_WRITE)
            .err()
            .expect("a file that is not a dma-buf must be refused");
        assert!(e.contains(&dev.name), "{e}");
        let e = dev
            .register_dmabuf(f.as_fd(), 0, 0, 0x1000_0000, Access::REMOTE_WRITE)
            .err()
            .expect("an empty region must be refused");
        assert!(e.contains("0 bytes"), "{e}");
    }

    #[test]
    fn list_devices() {
        let devices = RdmaDevice::list_devices().unwrap();
        println!("RDMA devices: {:?}", devices);
        // Always passes — just reports what's available.
    }

    #[test]
    fn gid_rank_prefers_routable_roce_v2_ipv4_by_type_not_position() {
        let link_local = {
            let mut g = [0u8; 16];
            g[0] = 0xfe;
            g[1] = 0x80;
            g[15] = 1;
            g
        };
        let ipv4 = {
            let mut g = [0u8; 16];
            g[10] = 0xff;
            g[11] = 0xff;
            g[12..].copy_from_slice(&[192, 168, 68, 60]);
            g
        };
        let mut ipv6 = [0u8; 16];
        ipv6[0] = 0x20;
        ipv6[1] = 0x01;
        ipv6[15] = 7;
        assert_eq!(gid_rank(&[0u8; 16], Some("RoCE v2")), None);
        assert_eq!(gid_rank(&link_local, Some("RoCE v2")), None);
        assert_eq!(gid_rank(&ipv4, Some("RoCE v2")), Some(0));
        assert_eq!(gid_rank(&ipv6, Some("RoCE v2")), Some(1));
        assert_eq!(gid_rank(&ipv4, Some("IB/RoCE v1")), Some(2));
        assert_eq!(gid_rank(&ipv6, None), Some(2));

        // Soft-RoCE layout seen on a dev host: link-local v2 at 0 and 2,
        // IPv4 v2 at 1. The old position-based scan (2..16) picked 0.
        let table = [
            (link_local, "RoCE v2"),
            (ipv4, "RoCE v2"),
            (link_local, "RoCE v2"),
        ];
        let best = table
            .iter()
            .enumerate()
            .filter_map(|(i, (g, t))| gid_rank(g, Some(t)).map(|r| (r, i)))
            .min()
            .map(|(_, i)| i);
        assert_eq!(best, Some(1));
    }

    #[test]
    fn open_all_active() {
        let devices = RdmaDevice::open_all();
        println!("Active RDMA devices: {}", devices.len());
        for dev in &devices {
            println!(
                "  {} port={} state={:?}",
                dev.name,
                dev.port,
                dev.port_state()
            );
        }
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn register_memory() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let mut buf = vec![0u8; 4096];
        let mr = dev
            .register_memory(buf.as_mut_ptr(), buf.len(), Access::REMOTE_WRITE)
            .unwrap();
        assert_ne!(mr.lkey, 0);
        assert_ne!(mr.rkey, 0);
        // The region keeps the PD and context alive past the
        // caller's device handle, so this order is sound.
        drop(dev);
        drop(mr);
    }

    #[test]
    fn access_grants_only_what_is_named() {
        assert!(!Access::LOCAL.remote_read() && !Access::LOCAL.remote_write());
        assert_eq!(Access::LOCAL.remote_bits(), 0);
        assert!(Access::REMOTE_WRITE.remote_write() && !Access::REMOTE_WRITE.remote_read());
        assert!(Access::REMOTE_READ.remote_read() && !Access::REMOTE_READ.remote_write());
        assert_eq!(
            Access::REMOTE_READ_WRITE.remote_bits(),
            (ffi::IBV_ACCESS_REMOTE_READ | ffi::IBV_ACCESS_REMOTE_WRITE) as u32
        );
        for a in [
            Access::LOCAL,
            Access::REMOTE_WRITE,
            Access::REMOTE_READ,
            Access::REMOTE_READ_WRITE,
        ] {
            assert_ne!(
                a.bits() & ffi::IBV_ACCESS_LOCAL_WRITE,
                0,
                "{a:?} must allow local DMA"
            );
            assert_eq!(a.bits() & ffi::IBV_ACCESS_REMOTE_ATOMIC, 0);
        }
    }

    #[test]
    fn routable_gid_excludes_zero_and_link_local() {
        let mut ll = [0u8; 16];
        ll[0] = 0xfe;
        ll[1] = 0x80;
        ll[15] = 1;
        assert!(!gid_is_routable(&[0; 16]));
        assert!(!gid_is_routable(&ll));
        let global = "fd00:1:3900:20::1"
            .parse::<std::net::Ipv6Addr>()
            .expect("v6")
            .octets();
        assert!(gid_is_routable(&global));
        assert!(gid_is_routable(
            &std::net::Ipv4Addr::new(10, 0, 0, 1)
                .to_ipv6_mapped()
                .octets()
        ));
    }
}
