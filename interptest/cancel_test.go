package interptest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/interp"
)

// A goroutine parked in a native call made by interpreted code (a socket
// read, an accept, an HTTP request, a pipe, a child process) stops once the
// interpreter is interrupted: the bound standard library ties those calls to
// the interpreter's context and closes what it opened.

// silentServer accepts TCP connections and never writes to them.
func silentServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
		for _, c := range conns {
			c.Close()
		}
	})
	return ln.Addr().String()
}

// hangingHTTP is an HTTP server whose handler blocks until the test ends.
func hangingHTTP(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); s.Close() })
	return s.URL
}

func hostAddr(addr string) map[string]map[string]reflect.Value {
	return map[string]map[string]reflect.Value{"hostaddr": {"Addr": reflect.ValueOf(addr)}}
}

// stopWithin interrupts i and fails unless its run ends and every
// goroutine stops within d.
func stopWithin(t *testing.T, i *interp.Interp, done <-chan error, d time.Duration) {
	t.Helper()
	t0 := time.Now()
	i.Interrupt()
	select {
	case err := <-done:
		if !errors.Is(err, interp.ErrInterrupted) {
			t.Fatalf("run ended with %v, want ErrInterrupted", err)
		}
	case <-time.After(d):
		t.Fatalf("run did not end within %s of the interrupt (%d goroutines running)", d, i.Running())
	}
	ctx, cancel := context.WithTimeout(context.Background(), d-time.Since(t0))
	defer cancel()
	if err := i.WaitStopped(ctx); err != nil {
		t.Fatalf("WaitStopped: %v", err)
	}
	t.Logf("stopped in %v", time.Since(t0).Round(time.Microsecond))
	i.CloseAndRelease()
}

func TestInterruptNativeBlocking(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		sleep = ""
	}
	cases := []struct {
		name string
		src  string
		addr func(*testing.T) string
	}{
		{"conn.Read on net.Dial", `package main
import ("net"; "hostaddr")
func main() {
	c, err := net.Dial("tcp", hostaddr.Addr)
	if err != nil { panic(err) }
	println("ready")
	c.Read(make([]byte, 1))
}`, silentServer},
		{"conn.Read on net.DialTimeout in a goroutine", `package main
import ("net"; "time"; "hostaddr")
func main() {
	c, err := net.DialTimeout("tcp", hostaddr.Addr, time.Second)
	if err != nil { panic(err) }
	go c.Read(make([]byte, 1))
	go func() { c.Read(make([]byte, 1)) }()
	println("ready")
	select {}
}`, silentServer},
		{"conn.Read on a net.Dialer", `package main
import ("context"; "net"; "hostaddr")
func main() {
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "tcp", hostaddr.Addr)
	if err != nil { panic(err) }
	println("ready")
	c.Read(make([]byte, 1))
}`, silentServer},
		{"http.Get", `package main
import ("net/http"; "hostaddr")
func main() { println("ready"); http.Get(hostaddr.Addr) }`, hangingHTTP},
		{"http.Client.Get", `package main
import ("net/http"; "hostaddr")
func main() { c := &http.Client{}; println("ready"); c.Get(hostaddr.Addr) }`, hangingHTTP},
		{"http.DefaultClient.Do", `package main
import ("net/http"; "hostaddr")
func main() {
	req, _ := http.NewRequest("GET", hostaddr.Addr, nil)
	println("ready")
	http.DefaultClient.Do(req)
}`, hangingHTTP},
		{"Accept", `package main
import "net"
func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { panic(err) }
	println("ready")
	ln.Accept()
}`, nil},
		{"Read on an accepted conn", `package main
import "net"
func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { panic(err) }
	go func() { c, _ := net.Dial("tcp", ln.Addr().String()); c.Read(make([]byte, 1)) }()
	c, err := ln.Accept()
	if err != nil { panic(err) }
	println("ready")
	c.Read(make([]byte, 1))
}`, nil},
		{"http.ListenAndServe", `package main
import ("net/http"; "time")
func main() {
	go http.ListenAndServe("127.0.0.1:0", nil)
	time.Sleep(10 * time.Millisecond)
	println("ready")
	select {}
}`, nil},
		{"http.Server.Serve", `package main
import ("net"; "net/http")
func main() {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.NotFoundHandler()}
	go func() { c, _ := net.Dial("tcp", ln.Addr().String()); c.Read(make([]byte, 1)) }()
	println("ready")
	srv.Serve(ln)
}`, nil},
		{"httptest server with a hanging handler", `package main
import ("net/http"; "net/http/httptest")
func main() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { select {} }))
	println("ready")
	http.Get(s.URL)
}`, nil},
		{"os.Pipe read", `package main
import "os"
func main() { r, _, _ := os.Pipe(); println("ready"); r.Read(make([]byte, 1)) }`, nil},
		{"io.Pipe read", `package main
import "io"
func main() { r, _ := io.Pipe(); println("ready"); r.Read(make([]byte, 1)) }`, nil},
		{"net.Pipe read", `package main
import "net"
func main() { a, _ := net.Pipe(); println("ready"); a.Read(make([]byte, 1)) }`, nil},
	}
	if sleep != "" {
		cases = append(cases, struct {
			name string
			src  string
			addr func(*testing.T) string
		}{"exec.Command(sleep).Run", `package main
import "os/exec"
func main() { println("ready"); exec.Command("sleep", "60").Run() }`, nil})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var extra []map[string]map[string]reflect.Value
			if c.addr != nil {
				extra = append(extra, hostAddr(c.addr(t)))
			}
			base := runtime.NumGoroutine()
			i, done, out := runInterruptible(t, c.src, extra...)
			waitStarted(t, out, "ready", done)
			time.Sleep(30 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("run ended before the interrupt: %v", err)
			default:
			}
			stopWithin(t, i, done, 2*time.Second)
			waitGoroutines(t, base)
		})
	}
}

// Interrupting one interpreter closes only what it opened.
func TestInterruptLeavesOtherInterpreterAlone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	src := `package main
import ("net"; "hostaddr")
func main() {
	c, err := net.Dial("tcp", hostaddr.Addr)
	if err != nil { panic(err) }
	println("ready")
	b := make([]byte, 5)
	n, err := c.Read(b)
	println("read", string(b[:n]), err == nil)
}`
	a, doneA, outA := runInterruptible(t, src, hostAddr(ln.Addr().String()))
	waitStarted(t, outA, "ready", doneA)
	b, doneB, outB := runInterruptible(t, src, hostAddr(ln.Addr().String()))
	waitStarted(t, outB, "ready", doneB)
	srvA, srvB := <-accepted, <-accepted
	defer srvA.Close()
	defer srvB.Close()

	stopWithin(t, a, doneA, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if b.Interrupted() || b.Context().Err() != nil {
		t.Fatal("interrupting a cancelled the other interpreter")
	}
	// b's connection still works: both server ends get the write, only b
	// is there to read it.
	for _, c := range []net.Conn{srvA, srvB} {
		_, _ = c.Write([]byte("hello"))
	}
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatalf("b: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b did not finish")
	}
	if !strings.Contains(outB.String(), "read hello true") {
		t.Fatalf("b's output %q", outB.String())
	}
	b.CloseAndRelease()
}

// The host's context interrupts the interpreter when it ends.
func TestInterruptHostContext(t *testing.T) {
	i, done, out := runInterruptible(t, `package main
import "io"
func main() { r, _ := io.Pipe(); println("ready"); r.Read(make([]byte, 1)) }`)
	ctx, cancel := context.WithCancel(context.Background())
	i.SetContext(ctx)
	waitStarted(t, out, "ready", done)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, interp.ErrInterrupted) {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the host context did not stop the run")
	}
	if i.Context().Err() == nil {
		t.Fatal("the interpreter's context is not cancelled")
	}
	if err := i.WaitStopped(context.Background()); err != nil {
		t.Fatal(err)
	}
	i.CloseAndRelease()
}

// A value the host binds itself is not replaced.
func TestCancellationKeepsHostBindings(t *testing.T) {
	called := false
	i, done, _ := runInterruptible(t, `package main
import "io"
func main() { io.Pipe() }`, map[string]map[string]reflect.Value{"io": {
		"Pipe": reflect.ValueOf(func() (*io.PipeReader, *io.PipeWriter) { called = true; return io.Pipe() }),
	}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("the host's io.Pipe binding was replaced")
	}
	i.CloseAndRelease()
}
