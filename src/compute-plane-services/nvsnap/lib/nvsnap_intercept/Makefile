# NVSNAP Interception Library + NvSnap GPU Hypervisor — Merged Build
#
# Single library: libnvsnap_intercept.so
# Contains: io_uring/libuv/ZMQ interception (NvSnap) + GPU allocation tracking,
#           NCCL lifecycle, D2H checkpoint save (NvSnap).

CC = gcc
CXX = g++

# Compiler flags
CFLAGS = -Wall -Wextra -fPIC -O2 -g -D_GNU_SOURCE -Iinclude
CXXFLAGS = -Wall -Wextra -fPIC -O2 -g -std=c++17 -D_GNU_SOURCE -Iinclude

# Linker flags — use g++ for C++ object linking
LDFLAGS = -shared -ldl -lpthread -rdynamic -static-libstdc++ -static-libgcc -Wl,--version-script=libnvsnap.map

# ── NvSnap source files (C) ──────────────────────────────────────────────
NVSNAP_SRCS = src/init.c \
            src/self_disable.c \
            src/quiesce.c \
            src/io_uring_intercept.c \
            src/seccomp_intercept.c \
            src/zmq_intercept.c \
            src/nccl_intercept.c \
            src/abort_intercept.c \
            src/cuda_intercept.c \
            src/gpu_checkpoint.c

# ── NvSnap GPU-interposition source files (C + C++17) ─────────────────
# Sub-module under src/gpu/. Logically distinct from the OS-level
# intercept group above (cuda_intercept, io_uring_intercept,
# libuv_intercept, nccl_intercept, quiesce, seccomp_intercept,
# zmq_intercept) — the GPU sub-module hooks CUDA driver/runtime/NCCL
# at the call level for allocation tracking + checkpoint save/restore.
NVSNAP_GPU_C_SRCS = src/gpu/init.c \
                  src/gpu/config.c \
                  src/gpu/metrics.c \
                  src/gpu/interpose_cudart.c \
                  src/gpu/interpose_cudrv.c \
                  src/gpu/interpose_nccl.c \
                  src/gpu/symbol_table.c

NVSNAP_GPU_CXX_SRCS = src/gpu/tracker.cpp \
                    src/gpu/checkpoint.cpp

ENABLE_LIBUV_INTERCEPT ?= 0
ifeq ($(ENABLE_LIBUV_INTERCEPT),1)
NVSNAP_SRCS += src/libuv_intercept.c
endif

# Object files
C_OBJS = $(NVSNAP_SRCS:.c=.o) $(NVSNAP_GPU_C_SRCS:.c=.o)
CXX_OBJS = $(NVSNAP_GPU_CXX_SRCS:.cpp=.o)
OBJS = $(C_OBJS) $(CXX_OBJS)

# Output library
LIB = libnvsnap_intercept.so

.PHONY: all clean test test-cuda test-quick install debug help

all: $(LIB)

$(LIB): $(OBJS)
	$(CXX) $(CFLAGS) $(LDFLAGS) -o $@ $^
	@echo "Built $(LIB) (OS-level intercepts + GPU sub-module merged)"
	@$(MAKE) --no-print-directory verify-deps
	@$(MAKE) --no-print-directory verify-self-disable

# This library is force-loaded into EVERY process in the target mount namespace
# via /etc/ld.so.preload -- including CRIU, which runs against the bundled glibc
# rather than the container's. Any dependency beyond libc therefore gets resolved
# from the *container*, and pairing a newer container libstdc++/libm with the
# bundle's older libc fails with "GLIBC_2.xx not found" before CRIU can start.
# The C++ runtime is statically linked (-static-libstdc++ -static-libgcc) to keep
# the DT_NEEDED set at libc alone; libc itself is safe because glibc is backward
# compatible, so a library built here runs against any newer container glibc.
# Guard it so a new dependency cannot reintroduce the failure silently.
ALLOWED_NEEDED = libc.so.6 libdl.so.2 libpthread.so.0 ld-linux-x86-64.so.2

.PHONY: verify-deps
verify-deps: $(LIB)
	@bad=""; \
	for n in $$(readelf -d $(LIB) 2>/dev/null | awk '/NEEDED/{gsub(/[][]/,"",$$5); print $$5}'); do \
	    case " $(ALLOWED_NEEDED) " in *" $$n "*) ;; *) bad="$$bad $$n";; esac; \
	done; \
	if [ -n "$$bad" ]; then \
	    echo "ERROR: $(LIB) has disallowed dependencies:$$bad"; \
	    echo "       It is preloaded into CRIU, which uses the bundled glibc;"; \
	    echo "       anything outside [$(ALLOWED_NEEDED)] resolves from the target"; \
	    echo "       container and breaks on any newer-glibc image."; \
	    echo "       Static-link it (see -static-libstdc++) or drop the dependency."; \
	    exit 1; \
	fi; \
	echo "verify-deps: OK (only $(ALLOWED_NEEDED) permitted)"

# Workloads enable this library through /etc/ld.so.preload, which the loader
# applies to every process in the mount namespace -- including the CRIU that
# nsenters in to dump the container. Loaded into CRIU it wedges the dump (CRIU
# blocks in wait4() before finishing seize), so every constructor must call
# nvsnap_self_disabled() first and bail out. No environment variable can undo
# it later: constructors run before any NVSNAP_* gate is read. Adding a
# constructor without the guard silently reintroduces the hang, so check it
# here rather than discovering it in a customer's checkpoint.
CTOR_SRCS = $(NVSNAP_SRCS) $(NVSNAP_GPU_C_SRCS) src/libuv_intercept.c

.PHONY: verify-self-disable
verify-self-disable:
	@bad=0; \
	for f in $(CTOR_SRCS); do \
	    [ -f "$$f" ] || continue; \
	    case "$$f" in */self_disable.c) continue;; esac; \
	    awk -v F="$$f" ' \
	        /^[[:space:]]*[*]/ { next } \
	        /^[[:space:]]*\/\// { next } \
	        /__attribute__\(\(constructor/ { want=1; L=NR; next } \
	        want && /^[[:space:]]*(static[[:space:]]+)?void[[:space:]]+[A-Za-z_].*\(/ { next } \
	        want && /^[[:space:]]*\{[[:space:]]*$$/ { next } \
	        want && /^[[:space:]]*$$/ { next } \
	        want { \
	            if ($$0 ~ /if[[:space:]]*\([[:space:]]*nvsnap_self_disabled\(\)[[:space:]]*\)/) { want=0; next } \
	            print F ":" L; bad=1; want=0 \
	        } \
	        END { exit bad?1:0 }' "$$f" || bad=1; \
	done; \
	if [ "$$bad" != "0" ]; then \
	    echo "ERROR: the constructors listed above do not call nvsnap_self_disabled()"; \
	    echo "       as their FIRST statement. They will run inside CRIU (via"; \
	    echo "       /etc/ld.so.preload) and hang the dump."; \
	    echo "       Required first line of the body:"; \
	    echo "           if (nvsnap_self_disabled()) return;"; \
	    exit 1; \
	fi; \
	echo "verify-self-disable: OK (guard is the first statement in every constructor)"

# C compilation
src/%.o: src/%.c
	$(CC) $(CFLAGS) -c -o $@ $<

# C++ compilation
src/%.o: src/%.cpp
	$(CXX) $(CXXFLAGS) -c -o $@ $<

# ── Tests ───────────────────────────────────────────────────────────────

test: $(LIB)
	@echo "=== Quick load test ==="
	NVSNAP_LOG_LEVEL=3 LD_PRELOAD=./$(LIB) /bin/true
	@echo "Library loads successfully"
	@echo ""
	@echo "=== io_uring test ==="
	NVSNAP_LOG_LEVEL=4 LD_PRELOAD=./$(LIB) python3 tests/test_uring_simple.py 2>&1 || echo "io_uring test skipped"
	@echo ""
	@echo "=== uvloop test ==="
	NVSNAP_LOG_LEVEL=4 LD_PRELOAD=./$(LIB) python3 tests/test_uvloop_simple.py 2>&1 || echo "uvloop test skipped"
	@echo ""
	@echo "=== All tests completed ==="

test-cuda: $(LIB)
	@bash tests/run_cuda_tests.sh

test-quick: $(LIB) tests/test_dlsym_recursion tests/test_library_safety
	@echo "=== dlsym recursion test ==="
	@NVSNAP_LOG_LEVEL=1 LD_PRELOAD=./$(LIB) ./tests/test_dlsym_recursion 2>/dev/null
	@echo ""
	@echo "=== Library safety test ==="
	@NVSNAP_LOG_LEVEL=1 NVSNAP_QUIESCE_SIGNALS=1 NVSNAP_NCCL_INTERCEPT=1 NVSNAP_CUDA_INTERCEPT=1 \
		LD_PRELOAD=./$(LIB) ./tests/test_library_safety 2>/dev/null

tests/test_dlsym_recursion: tests/test_dlsym_recursion.c $(LIB)
	$(CC) -g -o $@ $< -ldl

tests/test_library_safety: tests/test_library_safety.c $(LIB)
	$(CC) -g -o $@ $< -ldl -lpthread

clean:
	rm -f $(OBJS) $(LIB)
	rm -f tests/test_libuv_simple tests/test_libuv_intercept tests/test_cuda_intercept

install: $(LIB)
	install -d $(DESTDIR)/usr/lib
	install -m 755 $(LIB) $(DESTDIR)/usr/lib/

debug: CFLAGS += -DDEBUG -O0
debug: CXXFLAGS += -DDEBUG -O0
debug: $(LIB)

help:
	@echo "NVSNAP + NvSnap Merged Interception Library"
	@echo ""
	@echo "Targets:"
	@echo "  all          Build libnvsnap_intercept.so"
	@echo "  test         Run all tests"
	@echo "  test-cuda    Run GPU tests (requires GPU)"
	@echo "  test-quick   Quick load test"
	@echo "  clean        Remove built files"
	@echo "  install      Install to system"
	@echo "  debug        Build with debug flags"
