package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// settings persisted in the work dir.
type settings struct {
	Proxy string `json:"proxy"` // e.g. http://127.0.0.1:7890 or socks5://127.0.0.1:1080; "" = auto
}

func settingsPath() string { return filepath.Join(workDir(), "settings.json") }

func loadSettings() settings {
	var s settings
	b, err := os.ReadFile(settingsPath())
	if err == nil {
		json.Unmarshal(b, &s)
	}
	if v := os.Getenv("VPNGATE_PROXY"); v != "" && s.Proxy == "" {
		s.Proxy = v
	}
	return s
}

func saveSettings(s settings) {
	b, _ := json.Marshal(s)
	os.WriteFile(settingsPath(), b, 0600)
}

var curSettings = loadSettings()

// systemProxyURL reads the OS system proxy (what browsers use):
// Windows registry, macOS scutil. Returns "" when none/disabled.
func systemProxyURL() string {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`, "/v", "ProxyServer").CombinedOutput()
		if err != nil {
			return ""
		}
		out2, _ := exec.Command("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`, "/v", "ProxyEnable").CombinedOutput()
		if !strings.Contains(string(out2), "0x1") {
			return ""
		}
		for _, f := range strings.Fields(string(out)) {
			if strings.Contains(f, ":") && !strings.Contains(f, "REG_") && !strings.HasPrefix(f, "HKEY") {
				srv := f
				// per-protocol form: http=127.0.0.1:7890;https=...;socks=...
				if strings.Contains(srv, "=") {
					for _, part := range strings.Split(srv, ";") {
						if strings.HasPrefix(part, "http=") {
							srv = strings.TrimPrefix(part, "http=")
							break
						}
						if strings.HasPrefix(part, "socks=") {
							return "socks5://" + strings.TrimPrefix(part, "socks=")
						}
					}
				}
				if !strings.Contains(srv, "://") {
					srv = "http://" + srv
				}
				return srv
			}
		}
	case "darwin":
		out, err := exec.Command("scutil", "--proxy").CombinedOutput()
		if err != nil {
			return ""
		}
		kv := map[string]string{}
		for _, l := range strings.Split(string(out), "\n") {
			p := strings.SplitN(l, ":", 2)
			if len(p) == 2 {
				kv[strings.TrimSpace(p[0])] = strings.TrimSpace(p[1])
			}
		}
		if kv["HTTPEnable"] == "1" && kv["HTTPProxy"] != "" {
			return "http://" + kv["HTTPProxy"] + ":" + kv["HTTPPort"]
		}
		if kv["SOCKSEnable"] == "1" && kv["SOCKSProxy"] != "" {
			return "socks5://" + kv["SOCKSProxy"] + ":" + kv["SOCKSPort"]
		}
	}
	return ""
}

// minimal SOCKS5 client dialer (RFC 1928, no-auth and user/pass).
func socks5Dial(proxyAddr string, user, pass string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		c, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				c.Close()
			}
		}()
		c.SetDeadline(time.Now().Add(15 * time.Second))
		methods := []byte{0x00}
		if user != "" {
			methods = append(methods, 0x02)
		}
		c.Write(append([]byte{0x05, byte(len(methods))}, methods...))
		buf := make([]byte, 264)
		if _, err = io_ReadFull(c, buf[:2]); err != nil {
			return nil, err
		}
		if buf[0] != 0x05 {
			return nil, fmt.Errorf("socks5: bad version")
		}
		if buf[1] == 0x02 {
			if user == "" {
				return nil, fmt.Errorf("socks5: proxy requires auth")
			}
			auth := []byte{0x01, byte(len(user))}
			auth = append(auth, user...)
			auth = append(auth, byte(len(pass)))
			auth = append(auth, pass...)
			c.Write(auth)
			if _, err = io_ReadFull(c, buf[:2]); err != nil {
				return nil, err
			}
			if buf[1] != 0x00 {
				return nil, fmt.Errorf("socks5: auth failed")
			}
		} else if buf[1] != 0x00 {
			return nil, fmt.Errorf("socks5: no acceptable auth method")
		}
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, _ := strconv.Atoi(portStr)
		req := []byte{0x05, 0x01, 0x00}
		if ip := net.ParseIP(host); ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				req = append(req, 0x01)
				req = append(req, ip4...)
			} else {
				req = append(req, 0x04)
				req = append(req, ip.To16()...)
			}
		} else {
			req = append(req, 0x03, byte(len(host)))
			req = append(req, host...)
		}
		req = append(req, byte(port>>8), byte(port))
		c.Write(req)
		if _, err = io_ReadFull(c, buf[:4]); err != nil {
			return nil, err
		}
		if buf[1] != 0x00 {
			return nil, fmt.Errorf("socks5: connect refused, code %d", buf[1])
		}
		switch buf[3] {
		case 0x01:
			_, err = io_ReadFull(c, buf[:6])
		case 0x03:
			if _, err = io_ReadFull(c, buf[:1]); err == nil {
				_, err = io_ReadFull(c, buf[:int(buf[0])+2])
			}
		case 0x04:
			_, err = io_ReadFull(c, buf[:18])
		}
		if err != nil {
			return nil, err
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
}

func io_ReadFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// transportFor builds an http.Transport for a proxy URL ("" = direct).
func transportFor(proxy string) (*http.Transport, error) {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if proxy == "" {
		tr.Proxy = nil
		return tr, nil
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http", "https":
		tr.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		user, pw := "", ""
		if u.User != nil {
			user = u.User.Username()
			pw, _ = u.User.Password()
		}
		tr.Proxy = nil
		tr.DialContext = socks5Dial(u.Host, user, pw)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http:// or socks5://)", u.Scheme)
	}
	return tr, nil
}

type transportCandidate struct {
	Label string
	Proxy string // "" = direct
}

// candidateTransports lists the routes to try, best first:
// saved proxy -> system proxy -> env proxy -> common local proxy ports.
func candidateTransports() []transportCandidate {
	seen := map[string]bool{}
	var out []transportCandidate
	add := func(label, p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, transportCandidate{label, p})
	}
	// Explicit and system proxies come first: when the user has a proxy on,
	// a blocked direct route would otherwise waste ~30s before falling back.
	if s := strings.TrimSpace(curSettings.Proxy); s != "" {
		add("saved proxy "+s, s)
	}
	if s := systemProxyURL(); s != "" {
		add("system proxy "+s, s)
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(k); v != "" {
			add("env "+k, v)
			break
		}
	}
	add("direct", "")
	// common local proxy ports: sbgui/sing-box mixed 2080, Clash 7890/7897,
	// v2rayN http 10809, generic http 8080, socks 1080/10808.
	local := []string{
		"http://127.0.0.1:2080", "http://127.0.0.1:7890", "http://127.0.0.1:7897",
		"http://127.0.0.1:10809", "http://127.0.0.1:8080",
		"socks5://127.0.0.1:1080", "socks5://127.0.0.1:10808",
	}
	for _, p := range local {
		u, _ := url.Parse(p)
		c, err := net.DialTimeout("tcp", u.Host, 250*time.Millisecond)
		if err != nil {
			continue
		}
		c.Close()
		add("local proxy "+p, p)
	}
	return out
}
