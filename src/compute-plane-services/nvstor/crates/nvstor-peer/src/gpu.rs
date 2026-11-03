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

//! GPU memory for GPUDirect RDMA, through the CUDA driver API loaded at
//! run time (`dlopen("libcuda.so.1")`): nvstor links no CUDA, builds
//! without the toolkit, and runs unchanged on nodes without a GPU.
//!
//! - A [`GpuBuffer`] is `cuMemAlloc` memory on one GPU, exported as a
//!   dma-buf (`cuMemGetHandleForAddressRange`) for
//!   [`nvstor_rdma::region::RailRegion::dmabuf`].
//! - The NIC's writes are complete when the server's reply arrives, but a
//!   later copy or kernel is only guaranteed to see them if the device
//!   orders them for its own context; on a device that does not,
//!   [`GpuBuffer::make_visible`] flushes them (`cuFlushGPUDirectRDMAWrites`),
//!   and a device that can do neither is refused at allocation.
//! - The CPU may not be able to load from the memory (on some platforms a
//!   CPU load faults, and granting host access with `cuMemSetAccess` is
//!   CUDA_ERROR_NOT_SUPPORTED),
//!   so bytes are checked over a copy ([`GpuBuffer::copy_to_host`]) into
//!   pinned host memory ([`PinnedHost`]), on the calling thread's own
//!   stream (the `_ptds` entry points), so copies from several threads run
//!   in parallel.

use std::ffi::{c_char, c_int, c_uint, c_ulonglong, c_void, CStr};
use std::os::fd::{AsFd, BorrowedFd, FromRawFd, OwnedFd};
use std::sync::Arc;

use nvstor_rdma::rail::Rails;
use nvstor_rdma::region::{RailRegion, RegionSlice};

use crate::fetch::{Device, HostBuffer, Layout};

type CuResult = c_int;
type CuDevice = c_int;
type CuContext = *mut c_void;
type CuDevicePtr = c_ulonglong;

const CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_FLUSH_WRITES_OPTIONS: c_int = 117;
const CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_WRITES_ORDERING: c_int = 118;
const CU_DEVICE_ATTRIBUTE_DMA_BUF_SUPPORTED: c_int = 124;
/// `CU_GPU_DIRECT_RDMA_WRITES_ORDERING_OWNER`: the device's own context sees
/// third-party writes in order, with no flush.
const ORDERING_OWNER: c_int = 100;
/// `CU_FLUSH_GPU_DIRECT_RDMA_WRITES_OPTION_HOST`: the host may flush.
const FLUSH_OPTION_HOST: c_int = 1;
const CU_FLUSH_GPU_DIRECT_RDMA_WRITES_TARGET_CURRENT_CTX: c_int = 0;
const CU_FLUSH_GPU_DIRECT_RDMA_WRITES_TO_OWNER: c_int = 100;
const CU_MEM_RANGE_HANDLE_TYPE_DMA_BUF_FD: c_int = 1;
/// `CU_STREAM_PER_THREAD`: the calling thread's default stream.
const CU_STREAM_PER_THREAD: usize = 0x2;

/// `CUipcMemHandle`: 64 opaque bytes another process passes to
/// `cuIpcOpenMemHandle` to map the allocation.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct IpcHandle(pub [u8; 64]);
const CU_MEM_RANGE_FLAG_DMA_BUF_MAPPING_TYPE_PCIE: c_ulonglong = 1;

/// dma-buf ranges must start and end on a host page; 2 MiB covers every
/// page size in use (4 KiB, 64 KiB, 2 MiB) and is cuMemAlloc's granularity.
const ALIGN: usize = 2 << 20;

/// How the NIC reaches GPU memory through the dma-buf.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mapping {
    /// The driver's choice (on Grace systems: over the coherent C2C link).
    Default,
    /// Through the GPU's PCIe BAR (`CU_MEM_RANGE_FLAG_DMA_BUF_MAPPING_TYPE_PCIE`).
    Pcie,
}

impl std::str::FromStr for Mapping {
    type Err = String;
    fn from_str(s: &str) -> Result<Self, String> {
        match s {
            "default" => Ok(Mapping::Default),
            "pcie" => Ok(Mapping::Pcie),
            _ => Err(format!("GPU mapping {s:?}: expected default or pcie")),
        }
    }
}

/// The driver entry points used, resolved once.
struct Cuda {
    _lib: Library,
    get_error_name: unsafe extern "C" fn(CuResult, *mut *const c_char) -> CuResult,
    device_get: unsafe extern "C" fn(*mut CuDevice, c_int) -> CuResult,
    device_get_attribute: unsafe extern "C" fn(*mut c_int, c_int, CuDevice) -> CuResult,
    primary_ctx_retain: unsafe extern "C" fn(*mut CuContext, CuDevice) -> CuResult,
    primary_ctx_release: unsafe extern "C" fn(CuDevice) -> CuResult,
    ctx_set_current: unsafe extern "C" fn(CuContext) -> CuResult,
    mem_alloc: unsafe extern "C" fn(*mut CuDevicePtr, usize) -> CuResult,
    mem_free: unsafe extern "C" fn(CuDevicePtr) -> CuResult,
    mem_host_alloc: unsafe extern "C" fn(*mut *mut c_void, usize, c_uint) -> CuResult,
    mem_free_host: unsafe extern "C" fn(*mut c_void) -> CuResult,
    memset_d8: unsafe extern "C" fn(CuDevicePtr, u8, usize) -> CuResult,
    ctx_synchronize: unsafe extern "C" fn() -> CuResult,
    /// Per-thread default stream variant: copies from different threads
    /// do not serialize on the legacy default stream.
    memcpy_dtoh: unsafe extern "C" fn(*mut c_void, CuDevicePtr, usize) -> CuResult,
    flush_rdma_writes: unsafe extern "C" fn(c_int, c_int) -> CuResult,
    /// Per-thread default stream variant, followed by a stream synchronize.
    memcpy_htod: unsafe extern "C" fn(CuDevicePtr, *const c_void, usize) -> CuResult,
    stream_synchronize: unsafe extern "C" fn(*mut c_void) -> CuResult,
    ipc_get_mem_handle: unsafe extern "C" fn(*mut IpcHandle, CuDevicePtr) -> CuResult,
    get_handle_for_address_range:
        unsafe extern "C" fn(*mut c_void, CuDevicePtr, usize, c_int, c_ulonglong) -> CuResult,
}

/// A `dlopen`ed library, closed on drop.
struct Library(*mut c_void);

// SAFETY: the handle is only passed to dlsym/dlclose, which are thread-safe.
unsafe impl Send for Library {}
unsafe impl Sync for Library {}

impl Drop for Library {
    fn drop(&mut self) {
        // SAFETY: the handle came from dlopen and is closed once.
        unsafe { libc::dlclose(self.0) };
    }
}

impl Library {
    fn open(name: &CStr) -> Result<Self, String> {
        // SAFETY: name is NUL-terminated; dlopen has no other preconditions.
        let h = unsafe { libc::dlopen(name.as_ptr(), libc::RTLD_NOW | libc::RTLD_LOCAL) };
        if h.is_null() {
            return Err(format!(
                "cannot load {}: {}",
                name.to_string_lossy(),
                dlerror()
            ));
        }
        Ok(Self(h))
    }

    /// The address of `name`, to be cast to its C signature by the caller.
    fn symbol(&self, name: &CStr) -> Result<*mut c_void, String> {
        // SAFETY: the handle is live; name is NUL-terminated.
        let p = unsafe { libc::dlsym(self.0, name.as_ptr()) };
        if p.is_null() {
            return Err(format!("libcuda has no {}", name.to_string_lossy()));
        }
        Ok(p)
    }
}

fn dlerror() -> String {
    // SAFETY: dlerror returns null or a NUL-terminated thread-local string.
    let e = unsafe { libc::dlerror() };
    if e.is_null() {
        "unknown error".into()
    } else {
        unsafe { CStr::from_ptr(e) }.to_string_lossy().into_owned()
    }
}

macro_rules! sym {
    ($lib:expr, $name:literal, $ty:ty) => {
        // SAFETY: the symbol is the CUDA driver API function of that name,
        // with the C signature `$ty`.
        unsafe { std::mem::transmute::<*mut c_void, $ty>($lib.symbol($name)?) }
    };
}

impl Cuda {
    fn load() -> Result<Self, String> {
        let lib = Library::open(c"libcuda.so.1")?;
        let init = sym!(lib, c"cuInit", unsafe extern "C" fn(c_uint) -> CuResult);
        let cuda = Cuda {
            get_error_name: sym!(
                lib,
                c"cuGetErrorName",
                unsafe extern "C" fn(CuResult, *mut *const c_char) -> CuResult
            ),
            device_get: sym!(
                lib,
                c"cuDeviceGet",
                unsafe extern "C" fn(*mut CuDevice, c_int) -> CuResult
            ),
            device_get_attribute: sym!(
                lib,
                c"cuDeviceGetAttribute",
                unsafe extern "C" fn(*mut c_int, c_int, CuDevice) -> CuResult
            ),
            primary_ctx_retain: sym!(
                lib,
                c"cuDevicePrimaryCtxRetain",
                unsafe extern "C" fn(*mut CuContext, CuDevice) -> CuResult
            ),
            primary_ctx_release: sym!(
                lib,
                c"cuDevicePrimaryCtxRelease_v2",
                unsafe extern "C" fn(CuDevice) -> CuResult
            ),
            ctx_set_current: sym!(
                lib,
                c"cuCtxSetCurrent",
                unsafe extern "C" fn(CuContext) -> CuResult
            ),
            mem_alloc: sym!(
                lib,
                c"cuMemAlloc_v2",
                unsafe extern "C" fn(*mut CuDevicePtr, usize) -> CuResult
            ),
            mem_free: sym!(
                lib,
                c"cuMemFree_v2",
                unsafe extern "C" fn(CuDevicePtr) -> CuResult
            ),
            mem_host_alloc: sym!(
                lib,
                c"cuMemHostAlloc",
                unsafe extern "C" fn(*mut *mut c_void, usize, c_uint) -> CuResult
            ),
            mem_free_host: sym!(
                lib,
                c"cuMemFreeHost",
                unsafe extern "C" fn(*mut c_void) -> CuResult
            ),
            memset_d8: sym!(
                lib,
                c"cuMemsetD8_v2",
                unsafe extern "C" fn(CuDevicePtr, u8, usize) -> CuResult
            ),
            ctx_synchronize: sym!(lib, c"cuCtxSynchronize", unsafe extern "C" fn() -> CuResult),
            memcpy_dtoh: sym!(
                lib,
                c"cuMemcpyDtoH_v2_ptds",
                unsafe extern "C" fn(*mut c_void, CuDevicePtr, usize) -> CuResult
            ),
            flush_rdma_writes: sym!(
                lib,
                c"cuFlushGPUDirectRDMAWrites",
                unsafe extern "C" fn(c_int, c_int) -> CuResult
            ),
            memcpy_htod: sym!(
                lib,
                c"cuMemcpyHtoD_v2_ptds",
                unsafe extern "C" fn(CuDevicePtr, *const c_void, usize) -> CuResult
            ),
            stream_synchronize: sym!(
                lib,
                c"cuStreamSynchronize",
                unsafe extern "C" fn(*mut c_void) -> CuResult
            ),
            ipc_get_mem_handle: sym!(
                lib,
                c"cuIpcGetMemHandle",
                unsafe extern "C" fn(*mut IpcHandle, CuDevicePtr) -> CuResult
            ),
            get_handle_for_address_range: sym!(
                lib,
                c"cuMemGetHandleForAddressRange",
                unsafe extern "C" fn(
                    *mut c_void,
                    CuDevicePtr,
                    usize,
                    c_int,
                    c_ulonglong,
                ) -> CuResult
            ),
            _lib: lib,
        };
        // SAFETY: cuInit takes flags (must be 0) and is safe to call repeatedly.
        cuda.check(unsafe { init(0) }, "cuInit")?;
        Ok(cuda)
    }

    fn check(&self, r: CuResult, what: &str) -> Result<(), String> {
        if r == 0 {
            return Ok(());
        }
        let mut name: *const c_char = std::ptr::null();
        // SAFETY: cuGetErrorName writes a pointer to a static string or fails.
        let named = unsafe { (self.get_error_name)(r, &mut name) } == 0 && !name.is_null();
        let name = if named {
            unsafe { CStr::from_ptr(name) }
                .to_string_lossy()
                .into_owned()
        } else {
            "unknown".into()
        };
        Err(format!("{what} failed: {name} ({r})"))
    }

    fn attribute(&self, device: CuDevice, attr: c_int, what: &str) -> Result<c_int, String> {
        let mut v: c_int = 0;
        // SAFETY: out-pointer to a local; valid device.
        self.check(
            unsafe { (self.device_get_attribute)(&mut v, attr, device) },
            &format!("cuDeviceGetAttribute({what})"),
        )?;
        Ok(v)
    }
}

/// `cuMemAlloc` memory on one GPU, exported as a dma-buf. On drop the
/// dma-buf is closed, then the memory freed, then the context released.
pub struct GpuBuffer {
    cuda: Arc<Cuda>,
    device: CuDevice,
    ctx: CuContext,
    ptr: CuDevicePtr,
    len: usize,
    dmabuf: Option<OwnedFd>,
    /// The device does not order NIC writes for its own context: flush
    /// them before GPU work reads.
    flush: bool,
    /// The memory was allocated here (freed on drop), not borrowed.
    owned: bool,
}

// SAFETY: the context and device pointer are process-wide CUDA handles;
// every call that uses them first makes the context current on the
// calling thread.
unsafe impl Send for GpuBuffer {}
unsafe impl Sync for GpuBuffer {}

impl GpuBuffer {
    /// At least `len` bytes (rounded up to 2 MiB) on GPU `gpu` (the
    /// index among this process's visible GPUs, CUDA's order), zeroed, and
    /// exported as a dma-buf with `mapping`. Refused on a device that can
    /// neither order nor flush NIC writes.
    pub fn alloc(gpu: u32, len: usize, mapping: Mapping) -> Result<Self, String> {
        let len = len
            .checked_next_multiple_of(ALIGN)
            .filter(|&l| l > 0)
            .ok_or_else(|| format!("GPU buffer of {len} bytes"))?;
        let cuda = Arc::new(Cuda::load()?);
        let ordinal = c_int::try_from(gpu).map_err(|_| format!("GPU {gpu}"))?;
        let mut device: CuDevice = 0;
        // SAFETY: out-pointer to a local.
        cuda.check(
            unsafe { (cuda.device_get)(&mut device, ordinal) },
            &format!("cuDeviceGet({gpu})"),
        )?;
        if cuda.attribute(
            device,
            CU_DEVICE_ATTRIBUTE_DMA_BUF_SUPPORTED,
            "DMA_BUF_SUPPORTED",
        )? != 1
        {
            return Err(format!(
                "GPU {gpu} cannot export memory as a dma-buf (driver or kernel too old)"
            ));
        }
        let ordering = cuda.attribute(
            device,
            CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_WRITES_ORDERING,
            "GPU_DIRECT_RDMA_WRITES_ORDERING",
        )?;
        let flush = ordering < ORDERING_OWNER;
        if flush {
            let options = cuda.attribute(
                device,
                CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_FLUSH_WRITES_OPTIONS,
                "GPU_DIRECT_RDMA_FLUSH_WRITES_OPTIONS",
            )?;
            if options & FLUSH_OPTION_HOST == 0 {
                return Err(format!(
                    "GPU {gpu} neither orders GPUDirect RDMA writes nor lets the host flush them"
                ));
            }
        }
        let mut ctx: CuContext = std::ptr::null_mut();
        // SAFETY: out-pointer to a local; valid device.
        cuda.check(
            unsafe { (cuda.primary_ctx_retain)(&mut ctx, device) },
            "cuDevicePrimaryCtxRetain",
        )?;
        // From here Drop releases the context, frees the memory once
        // allocated and closes the dma-buf once exported, on every path.
        let mut buf = GpuBuffer {
            cuda,
            device,
            ctx,
            ptr: 0,
            len,
            dmabuf: None,
            flush,
            owned: true,
        };
        buf.current()?;
        let mut ptr: CuDevicePtr = 0;
        // SAFETY: out-pointer to a local; the context is current.
        buf.cuda.check(
            unsafe { (buf.cuda.mem_alloc)(&mut ptr, len) },
            &format!("cuMemAlloc({len})"),
        )?;
        buf.ptr = ptr;
        // A dma-buf range must start on a host page; cuMemAlloc promises
        // only 256 bytes, though allocations this size are 2 MiB aligned.
        // SAFETY: sysconf has no preconditions.
        let page = usize::try_from(unsafe { libc::sysconf(libc::_SC_PAGESIZE) })
            .unwrap_or(ALIGN)
            .max(1);
        if !(ptr as usize).is_multiple_of(page) {
            return Err(format!("cuMemAlloc returned {ptr:#x}, not aligned to the {page}-byte host page a dma-buf needs"));
        }
        // Zeroed, so a range that never landed cannot pass for old data.
        // SAFETY: the range is the allocation just made.
        buf.cuda
            .check(unsafe { (buf.cuda.memset_d8)(ptr, 0, len) }, "cuMemsetD8")?;
        // SAFETY: no arguments; the context is current.
        buf.cuda
            .check(unsafe { (buf.cuda.ctx_synchronize)() }, "cuCtxSynchronize")?;
        let flags = match mapping {
            Mapping::Default => 0,
            Mapping::Pcie => CU_MEM_RANGE_FLAG_DMA_BUF_MAPPING_TYPE_PCIE,
        };
        let mut fd: c_int = -1;
        // SAFETY: out-pointer to a local int (the handle type is an fd);
        // the range is the whole allocation, aligned.
        buf.cuda.check(
            unsafe {
                (buf.cuda.get_handle_for_address_range)(
                    (&mut fd as *mut c_int).cast(),
                    ptr,
                    len,
                    CU_MEM_RANGE_HANDLE_TYPE_DMA_BUF_FD,
                    flags,
                )
            },
            &format!("cuMemGetHandleForAddressRange({mapping:?})"),
        )?;
        if fd < 0 {
            return Err("cuMemGetHandleForAddressRange returned no fd".into());
        }
        // SAFETY: the driver handed us ownership of a new fd.
        buf.dmabuf = Some(unsafe { OwnedFd::from_raw_fd(fd) });
        Ok(buf)
    }

    /// `len` bytes at device address `ptr` on GPU `gpu`, allocated by
    /// someone else (an inference engine's own allocator), exported as a
    /// dma-buf with `mapping`. Nothing is allocated, zeroed or freed: the
    /// owner must keep the memory allocated for as long as this buffer,
    /// and every registration of its dma-buf, lives. `ptr` must lie on a
    /// host page and `len` be a whole number of host pages, as a dma-buf
    /// range requires.
    ///
    /// # Safety
    /// `ptr..ptr + len` must be device memory of GPU `gpu` that stays
    /// allocated until this buffer is dropped.
    pub unsafe fn borrowed(
        gpu: u32,
        ptr: u64,
        len: usize,
        mapping: Mapping,
    ) -> Result<Self, String> {
        if len == 0 {
            return Err("an empty GPU range".into());
        }
        // SAFETY: sysconf has no preconditions.
        let page = usize::try_from(unsafe { libc::sysconf(libc::_SC_PAGESIZE) })
            .unwrap_or(ALIGN)
            .max(1);
        if !(ptr as usize).is_multiple_of(page) || !len.is_multiple_of(page) {
            return Err(format!(
                "GPU range {ptr:#x}+{len} is not whole {page}-byte host pages, as a dma-buf needs"
            ));
        }
        let cuda = Arc::new(Cuda::load()?);
        let ordinal = c_int::try_from(gpu).map_err(|_| format!("GPU {gpu}"))?;
        let mut device: CuDevice = 0;
        // SAFETY: out-pointer to a local.
        cuda.check(
            unsafe { (cuda.device_get)(&mut device, ordinal) },
            &format!("cuDeviceGet({gpu})"),
        )?;
        if cuda.attribute(
            device,
            CU_DEVICE_ATTRIBUTE_DMA_BUF_SUPPORTED,
            "DMA_BUF_SUPPORTED",
        )? != 1
        {
            return Err(format!(
                "GPU {gpu} cannot export memory as a dma-buf (driver or kernel too old)"
            ));
        }
        let ordering = cuda.attribute(
            device,
            CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_WRITES_ORDERING,
            "GPU_DIRECT_RDMA_WRITES_ORDERING",
        )?;
        let flush = ordering < ORDERING_OWNER;
        if flush {
            let options = cuda.attribute(
                device,
                CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_FLUSH_WRITES_OPTIONS,
                "GPU_DIRECT_RDMA_FLUSH_WRITES_OPTIONS",
            )?;
            if options & FLUSH_OPTION_HOST == 0 {
                return Err(format!(
                    "GPU {gpu} neither orders GPUDirect RDMA writes nor lets the host flush them"
                ));
            }
        }
        let mut ctx: CuContext = std::ptr::null_mut();
        // SAFETY: out-pointer to a local; valid device.
        cuda.check(
            unsafe { (cuda.primary_ctx_retain)(&mut ctx, device) },
            "cuDevicePrimaryCtxRetain",
        )?;
        let mut buf = GpuBuffer {
            cuda,
            device,
            ctx,
            ptr,
            len,
            dmabuf: None,
            flush,
            owned: false,
        };
        buf.current()?;
        let flags = match mapping {
            Mapping::Default => 0,
            Mapping::Pcie => CU_MEM_RANGE_FLAG_DMA_BUF_MAPPING_TYPE_PCIE,
        };
        let mut fd: c_int = -1;
        // SAFETY: out-pointer to a local int (the handle type is an fd);
        // the caller vouches for the range.
        buf.cuda.check(
            unsafe {
                (buf.cuda.get_handle_for_address_range)(
                    (&mut fd as *mut c_int).cast(),
                    ptr,
                    len,
                    CU_MEM_RANGE_HANDLE_TYPE_DMA_BUF_FD,
                    flags,
                )
            },
            &format!("cuMemGetHandleForAddressRange({mapping:?})"),
        )?;
        if fd < 0 {
            return Err("cuMemGetHandleForAddressRange returned no fd".into());
        }
        // SAFETY: the driver handed us ownership of a new fd.
        buf.dmabuf = Some(unsafe { OwnedFd::from_raw_fd(fd) });
        Ok(buf)
    }

    /// Make this buffer's context current on the calling thread.
    fn current(&self) -> Result<(), String> {
        // SAFETY: the context is retained for the buffer's lifetime.
        self.cuda.check(
            unsafe { (self.cuda.ctx_set_current)(self.ctx) },
            "cuCtxSetCurrent",
        )
    }

    pub fn len(&self) -> usize {
        self.len
    }

    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    /// The device address of byte 0: the region's IOVA.
    pub fn device_ptr(&self) -> u64 {
        self.ptr
    }

    pub fn dmabuf(&self) -> Result<BorrowedFd<'_>, String> {
        self.dmabuf
            .as_ref()
            .map(|f| f.as_fd())
            .ok_or_else(|| "the GPU buffer has no dma-buf".to_string())
    }

    /// Make the NIC writes that have completed so far visible to work on
    /// this buffer's context. A no-op on a device that orders them itself.
    pub fn make_visible(&self) -> Result<(), String> {
        if !self.flush {
            return Ok(());
        }
        self.current()?;
        // SAFETY: plain enum arguments; the context is current.
        self.cuda.check(
            unsafe {
                (self.cuda.flush_rdma_writes)(
                    CU_FLUSH_GPU_DIRECT_RDMA_WRITES_TARGET_CURRENT_CTX,
                    CU_FLUSH_GPU_DIRECT_RDMA_WRITES_TO_OWNER,
                )
            },
            "cuFlushGPUDirectRDMAWrites",
        )
    }

    /// `len` bytes of pinned host memory to copy into.
    pub fn pinned(self: &Arc<Self>, len: usize) -> Result<PinnedHost, String> {
        self.current()?;
        let mut p: *mut c_void = std::ptr::null_mut();
        // SAFETY: out-pointer to a local; the context is current; flags 0.
        self.cuda.check(
            unsafe { (self.cuda.mem_host_alloc)(&mut p, len.max(1), 0) },
            &format!("cuMemHostAlloc({len})"),
        )?;
        if p.is_null() {
            return Err("cuMemHostAlloc returned no memory".into());
        }
        Ok(PinnedHost {
            gpu: Arc::clone(self),
            ptr: p.cast(),
            len,
        })
    }

    /// Copy `bytes` to byte `at` of the buffer, on the calling thread's
    /// stream, and wait for the copy.
    pub fn copy_from_host(&self, bytes: &[u8], at: usize) -> Result<(), String> {
        at.checked_add(bytes.len())
            .filter(|&end| end <= self.len)
            .ok_or_else(|| {
                format!(
                    "bytes {at}..+{} are outside the {}-byte GPU buffer",
                    bytes.len(),
                    self.len
                )
            })?;
        self.current()?;
        // SAFETY: `bytes` is a valid host range; the device range was
        // checked against the allocation above.
        self.cuda.check(
            unsafe {
                (self.cuda.memcpy_htod)(self.ptr + at as u64, bytes.as_ptr().cast(), bytes.len())
            },
            "cuMemcpyHtoD",
        )?;
        // SAFETY: the per-thread stream handle is a constant the driver defines.
        self.cuda.check(
            unsafe { (self.cuda.stream_synchronize)(CU_STREAM_PER_THREAD as *mut c_void) },
            "cuStreamSynchronize",
        )
    }

    /// The handle another process on this node opens (`cuIpcOpenMemHandle`)
    /// to map this buffer; valid while this buffer lives.
    pub fn ipc_handle(&self) -> Result<IpcHandle, String> {
        self.current()?;
        let mut h = IpcHandle([0; 64]);
        // SAFETY: out-pointer to a local of the driver's 64-byte type; the
        // pointer is this allocation's base.
        self.cuda.check(
            unsafe { (self.cuda.ipc_get_mem_handle)(&mut h, self.ptr) },
            "cuIpcGetMemHandle",
        )?;
        Ok(h)
    }

    /// Copy `out.len()` bytes from byte `at` of the buffer into `out`, on
    /// the calling thread's own stream. Call [`Self::make_visible`] first
    /// for bytes the NIC wrote.
    pub fn copy_to_host(&self, at: usize, out: &mut [u8]) -> Result<(), String> {
        at.checked_add(out.len())
            .filter(|&end| end <= self.len)
            .ok_or_else(|| {
                format!(
                    "bytes {at}..+{} are outside the {}-byte GPU buffer",
                    out.len(),
                    self.len
                )
            })?;
        self.current()?;
        // SAFETY: `out` is a valid host range of that length; the device
        // range was checked against the allocation above. Synchronous.
        self.cuda.check(
            unsafe {
                (self.cuda.memcpy_dtoh)(out.as_mut_ptr().cast(), self.ptr + at as u64, out.len())
            },
            "cuMemcpyDtoH",
        )
    }
}

impl Drop for GpuBuffer {
    fn drop(&mut self) {
        // The export goes first: no dma-buf outlives the memory it names.
        drop(self.dmabuf.take());
        // Errors here leave nothing to do but report: the process is
        // releasing what it holds.
        if self.ptr != 0 && self.owned {
            // SAFETY: the allocation made in alloc, freed once.
            let r = self.current().and_then(|()| {
                self.cuda
                    .check(unsafe { (self.cuda.mem_free)(self.ptr) }, "cuMemFree")
            });
            if let Err(e) = r {
                tracing::warn!(error = %e, "freeing the GPU buffer");
            }
        }
        // SAFETY: retained once in alloc.
        if let Err(e) = self.cuda.check(
            unsafe { (self.cuda.primary_ctx_release)(self.device) },
            "cuDevicePrimaryCtxRelease",
        ) {
            tracing::warn!(error = %e, "releasing the GPU context");
        }
    }
}

/// GPU memory a fetch lands in ([`crate::fetch::fetch_to_device`]): one
/// [`GpuBuffer`] laid out as the fetch's [`Layout`], its slot area
/// registered on every NIC of the fetch's rails (the files' area is never
/// registered: no peer can write there).
pub struct GpuDevice {
    /// Declared first: the NICs lose access before anything else goes.
    region: RailRegion,
    buf: Arc<GpuBuffer>,
    layout: Layout,
    gpu: u32,
}

impl GpuDevice {
    pub fn new(
        rails: &Arc<Rails>,
        gpu: u32,
        mapping: Mapping,
        layout: &Layout,
    ) -> Result<Self, String> {
        let len = usize::try_from(layout.len())
            .map_err(|_| format!("a {}-byte GPU buffer", layout.len()))?;
        let buf = Arc::new(GpuBuffer::alloc(gpu, len, mapping)?);
        let slots = layout
            .slots
            .checked_mul(layout.slot_len)
            .filter(|&n| n > 0)
            .ok_or_else(|| "a fetch into the GPU needs at least one landing slot".to_string())?;
        let region = RailRegion::dmabuf(
            rails,
            buf.dmabuf()?,
            layout.files_len,
            slots,
            buf.device_ptr() + layout.files_len,
            Arc::clone(&buf) as Arc<dyn Send + Sync>,
        )?;
        Ok(Self {
            region,
            buf,
            layout: layout.clone(),
            gpu,
        })
    }

    /// Landing slot `slot`, for [`nvstor_rdma::s3_rdma::RdmaSource::fetch_to`].
    pub fn slot(&self, slot: usize) -> Option<RegionSlice<'_>> {
        let at = slot.checked_mul(self.layout.slot_len)?;
        self.region.slice(at, self.layout.slot_len)
    }

    pub fn gpu(&self) -> u32 {
        self.gpu
    }

    pub fn buffer(&self) -> &Arc<GpuBuffer> {
        &self.buf
    }

    pub fn layout(&self) -> &Layout {
        &self.layout
    }

    fn slot_at(&self, slot: usize) -> Result<usize, String> {
        usize::try_from(self.layout.slot_offset(slot)).map_err(|_| format!("slot {slot}"))
    }
}

impl Device for GpuDevice {
    fn make_visible(&self) -> Result<(), String> {
        self.buf.make_visible()
    }

    fn read_slot(&self, slot: usize, out: &mut [u8]) -> Result<(), String> {
        self.buf.copy_to_host(self.slot_at(slot)?, out)
    }

    fn place(&self, bytes: &[u8], offset: u64) -> Result<(), String> {
        let to = usize::try_from(offset).map_err(|_| format!("offset {offset}"))?;
        if to
            .checked_add(bytes.len())
            .is_none_or(|end| end as u64 > self.layout.files_len)
        {
            return Err(format!(
                "placing {} bytes at {offset} leaves the files' area",
                bytes.len()
            ));
        }
        self.buf.copy_from_host(bytes, to)
    }

    fn read(&self, offset: u64, out: &mut [u8]) -> Result<(), String> {
        let at = usize::try_from(offset).map_err(|_| format!("offset {offset}"))?;
        self.buf.copy_to_host(at, out)
    }

    fn host_buffer(&self, len: usize) -> Result<HostBuffer, String> {
        Ok(Box::new(self.buf.pinned(len)?))
    }
}

/// Page-locked host memory from `cuMemHostAlloc`, freed on drop; holds
/// its GPU buffer (and so the context) alive.
pub struct PinnedHost {
    gpu: Arc<GpuBuffer>,
    ptr: *mut u8,
    len: usize,
}

// SAFETY: plain host memory owned by this value alone.
unsafe impl Send for PinnedHost {}

impl std::ops::Deref for PinnedHost {
    type Target = [u8];
    fn deref(&self) -> &[u8] {
        // SAFETY: `len` bytes allocated by cuMemHostAlloc, owned here.
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }
}

impl std::ops::DerefMut for PinnedHost {
    fn deref_mut(&mut self) -> &mut [u8] {
        // SAFETY: as above, and `&mut self` makes the borrow unique.
        unsafe { std::slice::from_raw_parts_mut(self.ptr, self.len) }
    }
}

impl Drop for PinnedHost {
    fn drop(&mut self) {
        // SAFETY: allocated by cuMemHostAlloc in `pinned`, freed once.
        let r = self.gpu.current().and_then(|()| {
            self.gpu.cuda.check(
                unsafe { (self.gpu.cuda.mem_free_host)(self.ptr.cast()) },
                "cuMemFreeHost",
            )
        });
        if let Err(e) = r {
            tracing::warn!(error = %e, "freeing pinned host memory");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mapping_parses() {
        assert_eq!("default".parse::<Mapping>(), Ok(Mapping::Default));
        assert_eq!("pcie".parse::<Mapping>(), Ok(Mapping::Pcie));
        assert!("c2c".parse::<Mapping>().is_err());
    }

    #[test]
    fn no_gpu_is_an_error_not_a_crash() {
        // On a host without libcuda (or without a GPU) alloc must fail
        // cleanly; on a GPU host it must succeed and copy back zeroes.
        match GpuBuffer::alloc(0, 1, Mapping::Default) {
            Ok(b) => {
                let b = Arc::new(b);
                assert_eq!(b.len(), ALIGN);
                assert!(b.dmabuf().is_ok());
                b.make_visible().expect("flush");
                let mut out = b.pinned(4096).expect("pinned");
                out.fill(1);
                b.copy_to_host(0, &mut out).expect("copy back");
                assert!(out.iter().all(|&x| x == 0));
                assert!(b.copy_to_host(ALIGN - 1, &mut out).is_err());
                let ones = vec![1u8; 4096];
                b.copy_from_host(&ones, 4096).expect("copy from host");
                b.copy_to_host(4096, &mut out).expect("copy back");
                assert!(out.iter().all(|&x| x == 1));
                assert!(b.copy_from_host(&ones, ALIGN - 1).is_err());
                assert!(b.ipc_handle().is_ok());
            }
            Err(e) => {
                // Hosts with a GPU must not take this branch (NVSTOR_TEST_GPU=1).
                assert!(
                    std::env::var_os("NVSTOR_TEST_GPU").is_none(),
                    "GPU expected: {e}"
                );
                assert!(!e.is_empty());
            }
        }
    }
}
