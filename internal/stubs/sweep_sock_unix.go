//go:build unix && !wasm

package stubs

import (
	"runtime/debug"
	"syscall"
)

// sockDumpBuf bounds the heap-dump prefix kept: the itab records come first,
// so a socket buffer this large holds them, and the rest of the dump fails to
// write at once instead of costing a write of the whole heap.
const sockDumpBuf = 8 << 20

// dumpHeadViaSocket writes a heap dump into a non-blocking socket pair and
// returns what fit in its buffer. ok is false if the socket could not be set
// up; the caller falls back to a file.
func dumpHeadViaSocket() (head []byte, ok bool) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = syscall.Close(fds[0]) }()
	defer func() { _ = syscall.Close(fds[1]) }()
	_ = syscall.SetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF, sockDumpBuf)
	_ = syscall.SetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_RCVBUF, sockDumpBuf)
	if syscall.SetNonblock(fds[0], true) != nil || syscall.SetNonblock(fds[1], true) != nil {
		return nil, false
	}
	debug.WriteHeapDump(uintptr(fds[0]))
	buf := make([]byte, 1<<16)
	for {
		n, err := syscall.Read(fds[1], buf)
		if n > 0 {
			head = append(head, buf[:n]...)
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return head, true
		}
	}
}
