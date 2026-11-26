package interp

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

// Tying blocking standard-library calls to the interpreter's context.
//
// An interrupt cannot reach a goroutine parked in native code. What can
// unblock it is what unblocks it in Go: cancelling the context its call
// carries, or closing the resource it waits on. Interpreted code often
// passes no context (net.Dial, http.Get, exec.Command) or ignores the one it
// has, so the interpreter binds those entry points to forms that carry its
// own context (vm.Machine.Context, cancelled by Interrupt), and registers
// every connection, listener, pipe, file and server they open to be closed
// on Interrupt (vm.CloseOnInterrupt), which also wakes a Read or Accept
// already parked on it.
//
// Package-level functions are rebound in the interpreter's own copy of the
// package's values, so two interpreters never share a context; a value the
// host bound itself with ImportPackageValues is left alone. Methods of
// native types (net.Dialer, net.ListenConfig, the TCP and Unix listeners,
// http.Client, http.Server) are intercepted with native method hooks, which
// are process-wide but run with the calling interpreter's machine.

// patchCancellation rebinds the blocking entry points of i's bridged
// standard library (see above).
func (i *Interp) patchCancellation() {
	m := i.Machine
	ictx := m.Context
	b := func(path, name string, v any) { i.rebind(path, name, v) }

	b("net", "Dial", func(network, address string) (net.Conn, error) {
		var d net.Dialer
		return trackConn(m)(d.DialContext(ictx(), network, address))
	})
	b("net", "DialTimeout", func(network, address string, timeout time.Duration) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		return trackConn(m)(d.DialContext(ictx(), network, address))
	})
	b("net", "Listen", func(network, address string) (net.Listener, error) {
		var lc net.ListenConfig
		return trackListener(m)(lc.Listen(ictx(), network, address))
	})
	b("net", "ListenPacket", func(network, address string) (net.PacketConn, error) {
		var lc net.ListenConfig
		return trackPacketConn(m)(lc.ListenPacket(ictx(), network, address))
	})
	b("net", "DialTCP", func(network string, laddr, raddr *net.TCPAddr) (*net.TCPConn, error) {
		c, err := net.DialTCP(network, laddr, raddr)
		vm.CloseOnInterrupt(m, c)
		return c, err
	})
	b("net", "DialUDP", func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error) {
		c, err := net.DialUDP(network, laddr, raddr)
		vm.CloseOnInterrupt(m, c)
		return c, err
	})
	b("net", "DialUnix", func(network string, laddr, raddr *net.UnixAddr) (*net.UnixConn, error) {
		c, err := net.DialUnix(network, laddr, raddr)
		vm.CloseOnInterrupt(m, c)
		return c, err
	})
	b("net", "ListenTCP", func(network string, laddr *net.TCPAddr) (*net.TCPListener, error) {
		l, err := net.ListenTCP(network, laddr)
		vm.CloseOnInterrupt(m, l)
		return l, err
	})
	b("net", "ListenUDP", func(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
		c, err := net.ListenUDP(network, laddr)
		vm.CloseOnInterrupt(m, c)
		return c, err
	})
	b("net", "ListenUnix", func(network string, laddr *net.UnixAddr) (*net.UnixListener, error) {
		l, err := net.ListenUnix(network, laddr)
		vm.CloseOnInterrupt(m, l)
		return l, err
	})
	b("net", "ListenUnixgram", func(network string, laddr *net.UnixAddr) (*net.UnixConn, error) {
		c, err := net.ListenUnixgram(network, laddr)
		vm.CloseOnInterrupt(m, c)
		return c, err
	})
	b("net", "Pipe", func() (net.Conn, net.Conn) {
		a, c := net.Pipe()
		m.OnInterrupt(func() { _ = a.Close(); _ = c.Close() })
		return a, c
	})

	b("crypto/tls", "Dial", func(network, addr string, config *tls.Config) (*tls.Conn, error) {
		d := tls.Dialer{Config: config}
		c, err := d.DialContext(ictx(), network, addr)
		tc, _ := c.(*tls.Conn)
		vm.CloseOnInterrupt(m, tc)
		return tc, err
	})
	b("crypto/tls", "DialWithDialer", func(dialer *net.Dialer, network, addr string, config *tls.Config) (*tls.Conn, error) {
		d := tls.Dialer{NetDialer: dialer, Config: config}
		c, err := d.DialContext(ictx(), network, addr)
		tc, _ := c.(*tls.Conn)
		vm.CloseOnInterrupt(m, tc)
		return tc, err
	})
	b("crypto/tls", "Listen", func(network, laddr string, config *tls.Config) (net.Listener, error) {
		var lc net.ListenConfig
		l, err := lc.Listen(ictx(), network, laddr)
		if err != nil {
			return nil, err
		}
		_, _ = trackListener(m)(l, nil)
		return tls.NewListener(l, config), nil
	})

	b("os", "Pipe", func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		vm.CloseOnInterrupt(m, r)
		vm.CloseOnInterrupt(m, w)
		return r, w, err
	})
	b("os", "Open", func(name string) (*os.File, error) {
		f, err := os.Open(name)
		vm.CloseOnInterrupt(m, f)
		return f, err
	})
	b("os", "OpenFile", func(name string, flag int, perm os.FileMode) (*os.File, error) {
		f, err := os.OpenFile(name, flag, perm)
		vm.CloseOnInterrupt(m, f)
		return f, err
	})
	b("io", "Pipe", func() (*io.PipeReader, *io.PipeWriter) {
		r, w := io.Pipe()
		vm.CloseOnInterrupt(m, r)
		vm.CloseOnInterrupt(m, w)
		return r, w
	})

	b("os/exec", "Command", func(name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ictx(), name, arg...)
	})

	b("net/http", "NewRequest", func(method, url string, body io.Reader) (*http.Request, error) {
		return http.NewRequestWithContext(ictx(), method, url, body)
	})
	b("net/http", "Get", func(url string) (*http.Response, error) { return clientGet(m, http.DefaultClient, url) })
	b("net/http", "Head", func(url string) (*http.Response, error) { return clientHead(m, http.DefaultClient, url) })
	b("net/http", "Post", func(url, contentType string, body io.Reader) (*http.Response, error) {
		return clientPost(m, http.DefaultClient, url, contentType, body)
	})
	b("net/http", "PostForm", func(url string, data url.Values) (*http.Response, error) {
		return clientPostForm(m, http.DefaultClient, url, data)
	})
	b("net/http", "ListenAndServe", func(addr string, handler http.Handler) error {
		return serve(m, &http.Server{Addr: addr, Handler: handler}, (*http.Server).ListenAndServe) //nolint:gosec // the server http.ListenAndServe/Serve builds
	})
	b("net/http", "ListenAndServeTLS", func(addr, certFile, keyFile string, handler http.Handler) error {
		return serve(m, &http.Server{Addr: addr, Handler: handler}, func(s *http.Server) error { //nolint:gosec // the server http.ListenAndServe/Serve builds
			return s.ListenAndServeTLS(certFile, keyFile)
		})
	})
	b("net/http", "Serve", func(l net.Listener, handler http.Handler) error {
		return serve(m, &http.Server{Handler: handler}, func(s *http.Server) error { return s.Serve(l) }) //nolint:gosec // the server http.ListenAndServe/Serve builds
	})

	b("net/http/httptest", "NewServer", func(h http.Handler) *httptest.Server {
		s := httptest.NewServer(h)
		m.OnInterrupt(s.Close)
		return s
	})
	b("net/http/httptest", "NewTLSServer", func(h http.Handler) *httptest.Server {
		s := httptest.NewTLSServer(h)
		m.OnInterrupt(s.Close)
		return s
	})
	b("net/http/httptest", "NewUnstartedServer", func(h http.Handler) *httptest.Server {
		s := httptest.NewUnstartedServer(h)
		m.OnInterrupt(s.Close)
		return s
	})
}

// rebind replaces the bridged value path.name in i's own package table by
// v, unless the host bound a value of its own there.
func (i *Interp) rebind(path, name string, v any) {
	pkg, ok := i.Packages[path]
	if !ok || !pkg.Bin {
		return
	}
	cur, ok := pkg.Values[name]
	if !ok {
		return
	}
	def, ok := stdlib.Values[path][name]
	if !ok {
		return
	}
	if rv := cur.Reflect(); rv.Kind() != reflect.Func || def.Kind() != reflect.Func || rv.Pointer() != def.Pointer() {
		return // the host's own binding
	}
	pkg.Values[name] = vm.FromReflect(reflect.ValueOf(v))
}

// trackConn registers a dialled connection to be closed on interrupt.
func trackConn(m *vm.Machine) func(net.Conn, error) (net.Conn, error) {
	return func(c net.Conn, err error) (net.Conn, error) {
		if c != nil {
			closeConnOnInterrupt(m, c)
		}
		return c, err
	}
}

func closeConnOnInterrupt(m *vm.Machine, c net.Conn) {
	switch c := c.(type) {
	case *net.TCPConn:
		vm.CloseOnInterrupt(m, c)
	case *net.UnixConn:
		vm.CloseOnInterrupt(m, c)
	case *net.UDPConn:
		vm.CloseOnInterrupt(m, c)
	case *net.IPConn:
		vm.CloseOnInterrupt(m, c)
	case *tls.Conn:
		vm.CloseOnInterrupt(m, c)
	default:
		m.OnInterrupt(func() { _ = c.Close() })
	}
}

func trackListener(m *vm.Machine) func(net.Listener, error) (net.Listener, error) {
	return func(l net.Listener, err error) (net.Listener, error) {
		switch l := l.(type) {
		case nil:
		case *net.TCPListener:
			vm.CloseOnInterrupt(m, l)
		case *net.UnixListener:
			vm.CloseOnInterrupt(m, l)
		default:
			m.OnInterrupt(func() { _ = l.Close() })
		}
		return l, err
	}
}

func trackPacketConn(m *vm.Machine) func(net.PacketConn, error) (net.PacketConn, error) {
	return func(c net.PacketConn, err error) (net.PacketConn, error) {
		switch c := c.(type) {
		case nil:
		case *net.UDPConn:
			vm.CloseOnInterrupt(m, c)
		case *net.IPConn:
			vm.CloseOnInterrupt(m, c)
		case *net.UnixConn:
			vm.CloseOnInterrupt(m, c)
		default:
			m.OnInterrupt(func() { _ = c.Close() })
		}
		return c, err
	}
}

// serve runs an http.Server, closed (listeners and connections) on
// interrupt.
func serve(m *vm.Machine, s *http.Server, run func(*http.Server) error) error {
	vm.CloseOnInterrupt(m, s)
	return run(s)
}

// withContext returns req carrying ctx and the interpreter's context: the
// request ends when either does. done releases the tie; it is called when
// the response body is closed.
func withContext(m *vm.Machine, req *http.Request) (*http.Request, func()) {
	ictx := m.Context()
	ctx := req.Context()
	if ctx == ictx || ctx == context.Background() || ctx == context.TODO() {
		return req.WithContext(ictx), func() {}
	}
	merged, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ictx, cancel)
	return req.WithContext(merged), func() { stop(); cancel() }
}

// do is (*http.Client).Do tied to the interpreter's context.
func do(m *vm.Machine, c *http.Client, req *http.Request) (*http.Response, error) {
	req, done := withContext(m, req)
	resp, err := c.Do(req)
	if err != nil || resp == nil {
		done()
		return resp, err
	}
	resp.Body = &releasingBody{ReadCloser: resp.Body, done: done}
	return resp, nil
}

type releasingBody struct {
	io.ReadCloser
	done func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.done()
	return err
}

func clientGet(m *vm.Machine, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return do(m, c, req)
}

func clientHead(m *vm.Machine, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	return do(m, c, req)
}

func clientPost(m *vm.Machine, c *http.Client, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return do(m, c, req)
}

func clientPostForm(m *vm.Machine, c *http.Client, url string, data url.Values) (*http.Response, error) {
	return clientPost(m, c, url, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
}

// Hooks on methods of native types, run with the calling machine.
func init() {
	connErr := func(c net.Conn, err error) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(&c).Elem(), reflect.ValueOf(&err).Elem()}
	}
	listenerErr := func(l net.Listener, err error) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(&l).Elem(), reflect.ValueOf(&err).Elem()}
	}
	packetErr := func(c net.PacketConn, err error) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(&c).Elem(), reflect.ValueOf(&err).Elem()}
	}
	respErr := func(r *http.Response, err error) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(r), reflect.ValueOf(&err).Elem()}
	}
	errOnly := func(err error) []reflect.Value { return []reflect.Value{reflect.ValueOf(&err).Elem()} }
	str := func(v reflect.Value) string { return v.String() }
	ctxArg := func(v reflect.Value) context.Context {
		return as[context.Context](v)
	}
	// merged returns a context that ends with ctx or the interpreter's, and
	// its release.
	merged := func(m *vm.Machine, ctx context.Context) (context.Context, func()) {
		ictx := m.Context()
		if ctx == nil || ctx == ictx || ctx == context.Background() || ctx == context.TODO() {
			return ictx, func() {}
		}
		c, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(ictx, cancel)
		return c, func() { stop(); cancel() }
	}

	vm.RegisterNativeMethodHook((*net.Dialer)(nil), "Dial", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		d := recv.Interface().(*net.Dialer)
		return connErr(trackConn(m)(d.DialContext(m.Context(), str(a[0]), str(a[1]))))
	})
	vm.RegisterNativeMethodHook((*net.Dialer)(nil), "DialContext", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		d := recv.Interface().(*net.Dialer)
		ctx, release := merged(m, ctxArg(a[0]))
		defer release() // a connection outlives its dial context
		return connErr(trackConn(m)(d.DialContext(ctx, str(a[1]), str(a[2]))))
	})
	vm.RegisterNativeMethodHook((*net.ListenConfig)(nil), "Listen", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		lc := recv.Interface().(*net.ListenConfig)
		ctx, release := merged(m, ctxArg(a[0]))
		defer release()
		return listenerErr(trackListener(m)(lc.Listen(ctx, str(a[1]), str(a[2]))))
	})
	vm.RegisterNativeMethodHook((*net.ListenConfig)(nil), "ListenPacket", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		lc := recv.Interface().(*net.ListenConfig)
		ctx, release := merged(m, ctxArg(a[0]))
		defer release()
		return packetErr(trackPacketConn(m)(lc.ListenPacket(ctx, str(a[1]), str(a[2]))))
	})
	vm.RegisterNativeMethodHook((*net.TCPListener)(nil), "Accept", func(m *vm.Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
		return connErr(trackConn(m)(recv.Interface().(*net.TCPListener).Accept()))
	})
	vm.RegisterNativeMethodHook((*net.TCPListener)(nil), "AcceptTCP", func(m *vm.Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
		c, err := recv.Interface().(*net.TCPListener).AcceptTCP()
		vm.CloseOnInterrupt(m, c)
		return []reflect.Value{reflect.ValueOf(c), reflect.ValueOf(&err).Elem()}
	})
	vm.RegisterNativeMethodHook((*net.UnixListener)(nil), "Accept", func(m *vm.Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
		return connErr(trackConn(m)(recv.Interface().(*net.UnixListener).Accept()))
	})
	vm.RegisterNativeMethodHook((*net.UnixListener)(nil), "AcceptUnix", func(m *vm.Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
		c, err := recv.Interface().(*net.UnixListener).AcceptUnix()
		vm.CloseOnInterrupt(m, c)
		return []reflect.Value{reflect.ValueOf(c), reflect.ValueOf(&err).Elem()}
	})

	client := func(recv reflect.Value) *http.Client { return recv.Interface().(*http.Client) }
	vm.RegisterNativeMethodHook((*http.Client)(nil), "Do", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		return respErr(do(m, client(recv), a[0].Interface().(*http.Request)))
	})
	vm.RegisterNativeMethodHook((*http.Client)(nil), "Get", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		return respErr(clientGet(m, client(recv), str(a[0])))
	})
	vm.RegisterNativeMethodHook((*http.Client)(nil), "Head", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		return respErr(clientHead(m, client(recv), str(a[0])))
	})
	vm.RegisterNativeMethodHook((*http.Client)(nil), "Post", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		body := as[io.Reader](a[2])
		return respErr(clientPost(m, client(recv), str(a[0]), str(a[1]), body))
	})
	vm.RegisterNativeMethodHook((*http.Client)(nil), "PostForm", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		data := as[url.Values](a[1])
		return respErr(clientPostForm(m, client(recv), str(a[0]), data))
	})

	server := func(recv reflect.Value) *http.Server { return recv.Interface().(*http.Server) }
	vm.RegisterNativeMethodHook((*http.Server)(nil), "ListenAndServe", func(m *vm.Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
		return errOnly(serve(m, server(recv), (*http.Server).ListenAndServe))
	})
	vm.RegisterNativeMethodHook((*http.Server)(nil), "ListenAndServeTLS", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		return errOnly(serve(m, server(recv), func(s *http.Server) error { return s.ListenAndServeTLS(str(a[0]), str(a[1])) }))
	})
	vm.RegisterNativeMethodHook((*http.Server)(nil), "Serve", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		l := as[net.Listener](a[0])
		return errOnly(serve(m, server(recv), func(s *http.Server) error { return s.Serve(l) }))
	})
	vm.RegisterNativeMethodHook((*http.Server)(nil), "ServeTLS", func(m *vm.Machine, recv reflect.Value, a []reflect.Value) []reflect.Value {
		l := as[net.Listener](a[0])
		return errOnly(serve(m, server(recv), func(s *http.Server) error { return s.ServeTLS(l, str(a[1]), str(a[2])) }))
	})
}

// as returns v's value as a T, or T's zero value.
func as[T any](v reflect.Value) T {
	var zero T
	if !v.IsValid() {
		return zero
	}
	if t, ok := reflect.TypeAssert[T](v); ok {
		return t
	}
	return zero
}
