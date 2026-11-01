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

// fetch sources, in order: the official API, then a public reader mirror
// of the same API (helps when vpngate.net itself is unreachable locally).
var apiSources = []struct{ Label, URL string }{
	{"official https", "https://www.vpngate.net/api/iphone/"},
	{"official http", "http://www.vpngate.net/api/iphone/"},
	{"mirror (jina) of official http", "https://r.jina.ai/http://www.vpngate.net/api/iphone/"},
	{"mirror (jina) of official https", "https://r.jina.ai/https://www.vpngate.net/api/iphone/"},
}

func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }

// fetchServers tries every (route x source) combination until one works.
// Every attempt is written to the connection log so failures are visible.
// Returns the servers and a human-readable route description.
func fetchServers() ([]Server, string, error) {
	var errs []string
	cands := candidateTransports()
	// Phase 1: official sources on every route. Phase 2: mirror sources.
	for _, phase := range []int{0, 1} {
		for _, cand := range cands {
			tr, err := transportFor(cand.Proxy)
			if err != nil {
				if phase == 0 {
					conn.logf("Fetch: skip route %s (%v)", cand.Label, err)
				}
				continue
			}
			client := &http.Client{Timeout: 14 * time.Second, Transport: tr}
			for _, src := range apiSources {
				if (phase == 0) == strings.HasPrefix(src.Label, "mirror") {
					continue
				}
				servers, err := fetchOne(client, src.URL)
				if err != nil {
					msg := fmt.Sprintf("%s via %s: %v", src.Label, cand.Label, shortErr(err))
					errs = append(errs, msg)
					conn.logf("Fetch failed: %s", msg)
					continue
				}
				via := src.Label + " via " + cand.Label
				conn.logf("Fetched %d servers (%s)", len(servers), via)
				return servers, via, nil
			}
		}
	}
	return nil, "", fmt.Errorf("all fetch routes failed: %s", strings.Join(errs, "; "))
}

func shortErr(err error) error {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return fmt.Errorf("%s", s)
}

func fetchOne(client *http.Client, u string) ([]Server, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vpngate-gui/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	servers, err := parseServers(string(body))
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("0 servers parsed")
	}
	return servers, nil
}

// parseServers parses the CSV payload. Format: a "*vpn_servers" line,
// a "#HostName,..." header line, data rows, and a trailing "*" line.
func parseServers(payload string) ([]Server, error) {
	// Locate the "#HostName,..." header line; ignore anything before it
	// (official "*vpn_servers" banner, mirror page wrappers, BOM, etc.).
	raw := strings.Split(payload, "\n")
	start := -1
	for i, l := range raw {
		if strings.HasPrefix(strings.TrimSpace(l), "#HostName,") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("header line not found in payload (%d bytes)", len(payload))
	}
	var lines []string
	for _, l := range raw[start:] {
		l = strings.TrimRight(strings.TrimSpace(l), "\r")
		if l == "" || l == "*" {
			continue
		}
		l = strings.TrimPrefix(l, "#")
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
	via       string
}

func (c *cache) get(force bool) ([]Server, time.Time, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && len(c.servers) > 0 && time.Since(c.fetchedAt) < 10*time.Minute {
		return c.servers, c.fetchedAt, c.via, nil
	}
	s, via, err := fetchServers()
	if err != nil {
		if len(c.servers) > 0 {
			return c.servers, c.fetchedAt, c.via + " (stale cache; refresh failed)", nil
		}
		return nil, time.Time{}, "", err
	}
	c.servers, c.fetchedAt, c.via = s, time.Now(), via
	return s, c.fetchedAt, via, nil
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
