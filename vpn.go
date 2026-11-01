package main

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is one VPN Gate relay server from the official API.
type Server struct {
	HostName     string  `json:"hostname"`
	IP           string  `json:"ip"`
	Score        int64   `json:"score"`
	Ping         int     `json:"ping"`  // ms, -1 = unknown
	Speed        int64   `json:"speed"` // bps
	SpeedMbps    float64 `json:"speed_mbps"`
	CountryLong  string  `json:"country_long"`
	CountryShort string  `json:"country_short"`
	NumSessions  int     `json:"sessions"`
	UptimeMs     int64   `json:"uptime_ms"`
	UptimeDays   float64 `json:"uptime_days"`
	TotalUsers   int64   `json:"total_users"`
	TotalTraffic int64   `json:"total_traffic"`
	LogType      string  `json:"log_type"`
	Operator     string  `json:"operator"`
	Message      string  `json:"message"`
	OVPNBase64   string  `json:"-"`
}

// OVPNConfig decodes the embedded OpenVPN config, if present.
func (s *Server) OVPNConfig() (string, error) {
	if s.OVPNBase64 == "" {
		return "", fmt.Errorf("no OpenVPN config embedded for %s", s.HostName)
	}
	b, err := base64.StdEncoding.DecodeString(s.OVPNBase64)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

var apiURLs = []string{
	"https://www.vpngate.net/api/iphone/",
	"http://www.vpngate.net/api/iphone/",
}

func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }

// fetchServers downloads and parses the VPN Gate server list.
func fetchServers() ([]Server, error) {
	var lastErr error
	client := &http.Client{Timeout: 30 * time.Second}
	for _, u := range apiURLs {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "vpngate-gui/1.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
			continue
		}
		servers, err := parseServers(string(body))
		if err != nil {
			lastErr = err
			continue
		}
		if len(servers) == 0 {
			lastErr = fmt.Errorf("%s: 0 servers parsed", u)
			continue
		}
		return servers, nil
	}
	return nil, lastErr
}

// parseServers parses the CSV payload. Format: a "*vpn_servers" line,
// a "#HostName,..." header line, data rows, and a trailing "*" line.
func parseServers(payload string) ([]Server, error) {
	var lines []string
	for _, l := range strings.Split(payload, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l == "*" || strings.HasPrefix(l, "*vpn") {
			continue
		}
		if strings.HasPrefix(l, "#") {
			l = strings.TrimPrefix(l, "#")
		}
		lines = append(lines, l)
	}
	if len(lines) < 2 {
		return nil, fmt.Errorf("payload too short")
	}
	r := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []Server
	for _, rec := range recs[1:] {
		if len(rec) < 15 {
			continue
		}
		ping := atoi(rec[3])
		speed := atoi64(rec[4])
		up := atoi64(rec[8])
		out = append(out, Server{
			HostName: rec[0], IP: rec[1], Score: atoi64(rec[2]), Ping: ping,
			Speed: speed, SpeedMbps: float64(speed) / 1e6,
			CountryLong: rec[5], CountryShort: rec[6], NumSessions: atoi(rec[7]),
			UptimeMs: up, UptimeDays: float64(up) / 86400000.0,
			TotalUsers: atoi64(rec[9]), TotalTraffic: atoi64(rec[10]),
			LogType: rec[11], Operator: rec[12], Message: rec[13], OVPNBase64: rec[14],
		})
	}
	return out, nil
}

// cache holds the last fetched list (VPN Gate asks clients not to poll often).
type cache struct {
	mu        sync.Mutex
	servers   []Server
	fetchedAt time.Time
}

func (c *cache) get(force bool) ([]Server, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && len(c.servers) > 0 && time.Since(c.fetchedAt) < 10*time.Minute {
		return c.servers, c.fetchedAt, nil
	}
	s, err := fetchServers()
	if err != nil {
		if len(c.servers) > 0 {
			return c.servers, c.fetchedAt, nil
		} // serve stale
		return nil, time.Time{}, err
	}
	c.servers, c.fetchedAt = s, time.Now()
	return s, c.fetchedAt, nil
}

func (c *cache) find(host string) (Server, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		if s.HostName == host {
			return s, true
		}
	}
	return Server{}, false
}
