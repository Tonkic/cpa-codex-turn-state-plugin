package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type connectRecord struct{ Target, Host, Auth string }

type testTunnel struct {
	URL   string
	Close func()
}

func tunnelServer(t *testing.T, records chan<- connectRecord) *testTunnel {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		buffer := bufio.NewReader(conn)
		line, err := buffer.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			return
		}
		record := connectRecord{Target: parts[1]}
		for {
			line, err = buffer.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
			key, value, _ := strings.Cut(line, ":")
			switch strings.ToLower(key) {
			case "host":
				record.Host = strings.TrimSpace(value)
			case "proxy-authorization":
				record.Auth = strings.TrimSpace(value)
			}
		}
		records <- record
		upstream, err := net.DialTimeout("tcp", record.Target, time.Second)
		if err != nil {
			return
		}
		defer upstream.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		copied := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffer)
			if tcp, ok := upstream.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
			close(copied)
		}()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-copied
	}()
	return &testTunnel{URL: "http://" + listener.Addr().String(), Close: func() { _ = listener.Close(); <-done }}
}

func TestHTTPChainConnectAuthorityAndNoCredentialLeak(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credentials reached origin")
		}
		if r.Host == "proxy.host:5000" {
			t.Error("CONNECT override changed origin Host")
		}
		_, _ = io.WriteString(w, "target-ok")
	}))
	defer target.Close()
	records := make(chan connectRecord, 4)
	second := tunnelServer(t, records)
	defer second.Close()
	first := tunnelServer(t, records)
	defer first.Close()
	u, _ := url.Parse(second.URL)
	u.User = url.UserPassword("user", "secret")
	transport := chainTransport(&proxyEndpoint{URL: first.URL}, proxyEndpoint{URL: u.String(), ConnectHost: "proxy.host:5000"})
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "target-ok" {
		t.Fatal("chain lost response body")
	}
	a, b := <-records, <-records
	secondURL, _ := url.Parse(second.URL)
	targetURL, _ := url.Parse(target.URL)
	if a.Target != secondURL.Host || a.Auth != "" {
		t.Fatal("first hop did not connect only to second proxy")
	}
	if b.Target != targetURL.Host || b.Host != "proxy.host:5000" || b.Auth == "" {
		t.Fatal("second CONNECT authority/Host/auth mismatch")
	}
}

func TestSOCKSIPv6AndRemoteDNS(t *testing.T) {
	for _, target := range []string{"[2001:db8::1]:443", "example.invalid:8443"} {
		t.Run(target, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan bool, 1)
			go func() {
				defer server.Close()
				_ = server.SetDeadline(time.Now().Add(time.Second))
				var greeting [3]byte
				_, err := io.ReadFull(server, greeting[:])
				if err != nil {
					done <- false
					return
				}
				_, _ = server.Write([]byte{5, 0})
				var head [4]byte
				_, _ = io.ReadFull(server, head[:])
				host, port, _ := net.SplitHostPort(target)
				var addr string
				if head[3] == 4 {
					b := make([]byte, 16)
					_, _ = io.ReadFull(server, b)
					addr = net.IP(b).String()
				} else if head[3] == 3 {
					var n [1]byte
					_, _ = io.ReadFull(server, n[:])
					b := make([]byte, n[0])
					_, _ = io.ReadFull(server, b)
					addr = string(b)
				}
				var p [2]byte
				_, _ = io.ReadFull(server, p[:])
				want := uint16(443)
				if port == "8443" {
					want = 8443
				}
				good := addr == host && binary.BigEndian.Uint16(p[:]) == want
				_, _ = server.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				done <- good
			}()
			u, _ := url.Parse("socks5h://127.0.0.1:1080")
			if err := socksConnect(client, u, target); err != nil {
				t.Fatal(err)
			}
			if !<-done {
				t.Fatal("wrong SOCKS address")
			}
		})
	}
}

func TestProxyCancellationInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()
	tr := chainTransport(nil, proxyEndpoint{URL: "http://" + listener.Addr().String()})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = tr.DialContext(ctx, "tcp", "example.invalid:443")
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation did not interrupt CONNECT")
	}
	<-done
}

func TestCONNECTPreservesCoalescedTunnelBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\n\r\nhello")
	}()
	u, _ := url.Parse("http://proxy.invalid")
	conn, err := tunnelProxy(context.Background(), client, u, "example.invalid:443", "")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err = io.ReadFull(conn, b); err != nil || string(b) != "hello" {
		t.Fatal("lost buffered tunnel bytes")
	}
}

func TestLiveProxyChain(t *testing.T) {
	if os.Getenv("CPA_LIVE_PROXY_URL") == "" {
		t.Skip("explicit live proxy required")
	}
	tr := chainTransport(&proxyEndpoint{URL: "socks5h://127.0.0.1:1080"}, proxyEndpoint{URLEnv: "CPA_LIVE_PROXY_URL", ConnectHost: "proxy.example.invalid:8080"})
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 20 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		t.Fatal("live chain failed (details suppressed)")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil || resp.StatusCode != 200 || net.ParseIP(strings.TrimSpace(string(b))) == nil {
		t.Fatal("no valid IP response")
	}
	t.Logf("HTTPS status=%d exit=%s", resp.StatusCode, strings.TrimSpace(string(b)))
}
