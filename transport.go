package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type proxyEndpoint struct {
	URL         string `yaml:"url"`
	URLFile     string `yaml:"url_file"`
	URLEnv      string `yaml:"url_env"`
	ConnectHost string `yaml:"connect_host"`
}

func (p proxyEndpoint) resolve() (*url.URL, error) {
	raw := p.URL
	if p.URLFile != "" {
		f, err := os.Open(p.URLFile)
		if err != nil {
			return nil, errors.New("proxy secret file unavailable")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 8193))
		if err != nil || len(data) > 8192 {
			return nil, errors.New("proxy secret file invalid")
		}
		raw = strings.TrimSpace(string(data))
	}
	if p.URLEnv != "" {
		raw = os.Getenv(p.URLEnv)
	}
	if strings.Contains(raw, "{session}") {
		var id [4]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, errors.New("session generation failed")
		}
		raw = strings.ReplaceAll(raw, "{session}", hex.EncodeToString(id[:]))
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid proxy URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("unsupported proxy scheme")
	}
	if u.Port() != "" {
		n, e := strconv.Atoi(u.Port())
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid proxy port")
		}
	}
	return u, nil
}

func proxyAddress(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// The transport dials ONLY the first hop (or the selected proxy if no first hop).
// No environment proxies or direct fallback are used. CONNECT covers every TCP
// target port the proxy allows; IPv6 literals and remote DNS are supported.
func chainTransport(first *proxyEndpoint, second proxyEndpoint) *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		DisableKeepAlives:      true,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			upstream, err := second.resolve()
			if err != nil {
				return nil, err
			}
			start := upstream
			var front *url.URL
			if first != nil {
				front, err = first.resolve()
				if err != nil {
					return nil, err
				}
				start = front
			}
			conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", proxyAddress(start))
			if err != nil {
				return nil, errors.New("proxy TCP dial failed")
			}
			rawConn := conn
			// Cancellation must interrupt SOCKS and CONNECT reads, not only dial.
			stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
			good := false
			defer func() {
				stop()
				if !good {
					_ = rawConn.Close()
				}
			}()
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
			} else {
				_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
			}
			if front != nil {
				conn, err = tunnelProxy(ctx, conn, front, proxyAddress(upstream), first.ConnectHost)
				if err != nil {
					return nil, err
				}
			}
			conn, err = tunnelProxy(ctx, conn, upstream, target, second.ConnectHost)
			if err != nil {
				return nil, err
			}
			_ = conn.SetDeadline(time.Time{})
			good = true
			return conn, nil
		},
	}
}

func tunnelProxy(ctx context.Context, conn net.Conn, proxy *url.URL, target, hostOverride string) (net.Conn, error) {
	if proxy.Scheme == "socks5" || proxy.Scheme == "socks5h" {
		return conn, socksConnect(conn, proxy, target)
	}
	if proxy.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, errors.New("proxy TLS handshake failed")
		}
		conn = secured
	}
	host := target
	if hostOverride != "" {
		host = hostOverride
	}
	if strings.ContainsAny(host+target, "\r\n") {
		return nil, errors.New("invalid CONNECT authority")
	}
	var auth string
	if proxy.User != nil {
		pass, _ := proxy.User.Password()
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+pass)) + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%sProxy-Connection: Keep-Alive\r\n\r\n", target, host, auth); err != nil {
		return nil, errors.New("proxy CONNECT write failed")
	}
	// Read just the header. Do not lose coalesced tunnel bytes or consume them as
	// a CONNECT response body; some targets send data immediately after 200.
	header := make([]byte, 0, 1024)
	for len(header) < 64<<10 {
		// One byte at a time on the underlying conn prevents reading tunnel data
		// into a temporary HTTP parser buffer. CONNECT headers are small.
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return nil, errors.New("proxy CONNECT response failed")
		}
		header = append(header, b[0])
		if len(header) >= 4 && string(header[len(header)-4:]) == "\r\n\r\n" {
			break
		}
	}
	if !strings.HasSuffix(string(header), "\r\n\r\n") {
		return nil, errors.New("proxy CONNECT headers too large")
	}
	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(header))), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, errors.New("invalid proxy CONNECT response")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("proxy CONNECT status %d", resp.StatusCode)
	}
	return conn, nil
}

func socksConnect(conn net.Conn, proxy *url.URL, target string) error {
	methods := []byte{5, 1, 0}
	if proxy.User != nil {
		methods = []byte{5, 1, 2}
	}
	if _, err := conn.Write(methods); err != nil {
		return errors.New("SOCKS greeting write failed")
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 5 {
		return errors.New("SOCKS greeting failed")
	}
	if reply[1] == 2 && proxy.User != nil {
		user := proxy.User.Username()
		pass, _ := proxy.User.Password()
		if len(user) > 255 || len(pass) > 255 {
			return errors.New("SOCKS credentials too long")
		}
		msg := append([]byte{1, byte(len(user))}, []byte(user)...)
		msg = append(msg, byte(len(pass)))
		msg = append(msg, []byte(pass)...)
		if _, err := conn.Write(msg); err != nil {
			return errors.New("SOCKS auth write failed")
		}
		if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{1, 0} {
			return errors.New("SOCKS auth failed")
		}
	} else if reply[1] != 0 || proxy.User != nil {
		return errors.New("SOCKS auth method rejected")
	}
	host, portRaw, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid SOCKS target")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid SOCKS port")
	}
	msg := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			msg = append(msg, 1)
			msg = append(msg, v4...)
		} else {
			msg = append(msg, 4)
			msg = append(msg, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return errors.New("invalid SOCKS hostname")
		}
		msg = append(msg, 3, byte(len(host)))
		msg = append(msg, []byte(host)...)
	}
	msg = binary.BigEndian.AppendUint16(msg, uint16(port))
	if _, err := conn.Write(msg); err != nil {
		return errors.New("SOCKS CONNECT write failed")
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 5 || head[1] != 0 {
		return errors.New("SOCKS CONNECT rejected")
	}
	n := 0
	switch head[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return errors.New("SOCKS address failed")
		}
		n = int(b[0])
	default:
		return errors.New("invalid SOCKS address")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(n+2)); err != nil {
		return errors.New("SOCKS address truncated")
	}
	return nil
}
