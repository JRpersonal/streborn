package webui

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// protocolListener lets the existing agent port serve ordinary HTTP to the
// phone and TLS to the desktop app. TLS connections are identified by the
// first byte of the TLS record before either HTTP server receives them.
type protocolListener struct {
	base net.Listener
	http chan net.Conn
	tls  chan net.Conn
	done chan struct{}
	once sync.Once
}

func newProtocolListener(base net.Listener) *protocolListener {
	p := &protocolListener{base: base, http: make(chan net.Conn), tls: make(chan net.Conn), done: make(chan struct{})}
	go p.dispatch()
	return p
}

func (p *protocolListener) dispatch() {
	for {
		conn, err := p.base.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			reader := bufio.NewReader(c)
			first, err := reader.Peek(1)
			_ = c.SetReadDeadline(time.Time{})
			if err != nil {
				_ = c.Close()
				return
			}
			wrapped := &bufferedConn{Conn: c, reader: reader}
			out := p.http
			if first[0] == 0x16 {
				out = p.tls
			}
			select {
			case out <- wrapped:
			case <-p.done:
				_ = c.Close()
			}
		}(conn)
	}
}

func (p *protocolListener) HTTP() net.Listener { return protocolSubListener{parent: p, ch: p.http} }
func (p *protocolListener) TLS() net.Listener  { return protocolSubListener{parent: p, ch: p.tls} }

func (p *protocolListener) Close() error {
	var err error
	p.once.Do(func() {
		close(p.done)
		err = p.base.Close()
	})
	return err
}

type protocolSubListener struct {
	parent *protocolListener
	ch     <-chan net.Conn
}

func (l protocolSubListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ch:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	case <-l.parent.done:
		return nil, net.ErrClosed
	}
}

func (l protocolSubListener) Close() error   { return l.parent.Close() }
func (l protocolSubListener) Addr() net.Addr { return l.parent.base.Addr() }

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
