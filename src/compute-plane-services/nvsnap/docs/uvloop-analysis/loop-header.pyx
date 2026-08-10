# cython: language_level=3, embedsignature=True

import asyncio
cimport cython

from .includes.debug cimport UVLOOP_DEBUG
from .includes cimport uv
from .includes cimport system
from .includes.python cimport (
    PY_VERSION_HEX,
    PyMem_RawMalloc, PyMem_RawFree,
    PyMem_RawCalloc, PyMem_RawRealloc,
    PyUnicode_EncodeFSDefault,
    PyErr_SetInterrupt,
    _Py_RestoreSignals,
    Context_CopyCurrent,
    Context_Enter,
    Context_Exit,
    PyMemoryView_FromMemory, PyBUF_WRITE,
    PyMemoryView_FromObject, PyMemoryView_Check,
    PyOS_AfterFork_Parent, PyOS_AfterFork_Child,
    PyOS_BeforeFork,
    PyUnicode_FromString
)
from .includes.flowcontrol cimport add_flowcontrol_defaults

from libc.stdint cimport uint64_t
from libc.string cimport memset, strerror, memcpy
from libc cimport errno

from cpython cimport PyObject
from cpython cimport PyErr_CheckSignals, PyErr_Occurred
from cpython cimport PyThread_get_thread_ident
from cpython cimport Py_INCREF, Py_DECREF, Py_XDECREF, Py_XINCREF
from cpython cimport (
    PyObject_GetBuffer, PyBuffer_Release, PyBUF_SIMPLE,
    Py_buffer, PyBytes_AsString, PyBytes_CheckExact,
    PyBytes_AsStringAndSize,
    Py_SIZE, PyBytes_AS_STRING, PyBUF_WRITABLE
)
from cpython.pycapsule cimport PyCapsule_New, PyCapsule_GetPointer

from . import _noop


include "includes/stdlib.pxi"

include "errors.pyx"

cdef:
    int PY39 = PY_VERSION_HEX >= 0x03090000
    int PY311 = PY_VERSION_HEX >= 0x030b0000
    int PY313 = PY_VERSION_HEX >= 0x030d0000
    uint64_t MAX_SLEEP = 3600 * 24 * 365 * 100


cdef _is_sock_stream(sock_type):
    if SOCK_NONBLOCK == -1:
        return sock_type == uv.SOCK_STREAM
    else:
        # Linux's socket.type is a bitmask that can include extra info
        # about socket (like SOCK_NONBLOCK bit), therefore we can't do simple
        # `sock_type == socket.SOCK_STREAM`, see
        # https://github.com/torvalds/linux/blob/v4.13/include/linux/net.h#L77
        # for more details.
        return (sock_type & 0xF) == uv.SOCK_STREAM


cdef _is_sock_dgram(sock_type):
    if SOCK_NONBLOCK == -1:
        return sock_type == uv.SOCK_DGRAM
    else:
        # Read the comment in `_is_sock_stream`.
        return (sock_type & 0xF) == uv.SOCK_DGRAM


cdef isfuture(obj):
    if aio_isfuture is None:
        return isinstance(obj, aio_Future)
    else:
        return aio_isfuture(obj)


cdef inline socket_inc_io_ref(sock):
    if isinstance(sock, socket_socket):
        sock._io_refs += 1


cdef inline socket_dec_io_ref(sock):
    if isinstance(sock, socket_socket):
        sock._decref_socketios()


cdef inline run_in_context(context, method):
    # This method is internally used to workaround a reference issue that in
    # certain circumstances, inlined context.run() will not hold a reference to
    # the given method instance, which - if deallocated - will cause segfault.
    # See also: edgedb/edgedb#2222
    Py_INCREF(method)
    try:
        return context.run(method)
    finally:
        Py_DECREF(method)


cdef inline run_in_context1(context, method, arg):
    Py_INCREF(method)
    try:
        return context.run(method, arg)
    finally:
        Py_DECREF(method)


cdef inline run_in_context2(context, method, arg1, arg2):
    Py_INCREF(method)
    try:
        return context.run(method, arg1, arg2)
    finally:
        Py_DECREF(method)


# Used for deprecation and removal of `loop.create_datagram_endpoint()`'s
# *reuse_address* parameter
_unset = object()


@cython.no_gc_clear
cdef class Loop:
    def __cinit__(self):
        cdef int err

        # Install PyMem* memory allocators if they aren't installed yet.
        __install_pymem()

        # Install pthread_atfork handlers
        __install_atfork()

        self.uvloop = <uv.uv_loop_t*>PyMem_RawMalloc(sizeof(uv.uv_loop_t))
        if self.uvloop is NULL:
            raise MemoryError()

        self.slow_callback_duration = 0.1

        self._closed = 0
        self._debug = 0
        self._thread_id = 0
        self._running = 0
        self._stopping = 0

        self._transports = weakref_WeakValueDictionary()
        self._processes = set()

        # Used to keep a reference (and hence keep the fileobj alive)
        # for as long as its registered by add_reader or add_writer.
        # This is how the selector module and hence asyncio behaves.
        self._fd_to_reader_fileobj = {}
        self._fd_to_writer_fileobj = {}

        self._unix_server_sockets = {}

        self._timers = set()
        self._polls = {}

        self._recv_buffer_in_use = 0

        err = uv.uv_loop_init(self.uvloop)
        if err < 0:
            raise convert_error(err)
        self.uvloop.data = <void*> self

        self._init_debug_fields()

        self.active_process_handler = None

        self._last_error = None

        self._task_factory = None
        self._exception_handler = None
        self._default_executor = None

        self._queued_streams = set()
        self._executing_streams = set()
        self._ready = col_deque()
        self._ready_len = 0

        self.handler_async = UVAsync.new(
            self, <method_t>self._on_wake, self)

        self.handler_idle = UVIdle.new(
            self,
            new_MethodHandle(
                self, "loop._on_idle", <method_t>self._on_idle, None, self))

        # Needed to call `UVStream._exec_write` for writes scheduled
        # during `Protocol.data_received`.
        self.handler_check__exec_writes = UVCheck.new(
            self,
            new_MethodHandle(
                self, "loop._exec_queued_writes",
                <method_t>self._exec_queued_writes, None, self))
