// vpgui: VPN Gate quick-launcher GUI.
// Single binary, embedded web dashboard (default http://127.0.0.1:20124).
// Fetches the official VPN Gate relay list and connects with one click via
// L2TP/IPsec, OpenVPN, MS-SSTP or SoftEther (SSL-VPN).
//
// Usage: ./vpgui [-port 20124] [-nobrowser]
//
//	./vpgui -fetch        (CLI smoke test: list counts and exit)
//	./vpgui -version
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

//go:embed web/dashboard.html
var dashboardHTML string

const version = "1.0.1"

var store = &cache{}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func apiServers(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	servers, at, via, err := store.get(force)
	if err != nil {
		// Always JSON so the dashboard can show the error instead of hanging.
		writeJSON(w, map[string]interface{}{"error": err.Error(), "servers": []Server{}, "count": 0, "countries": map[string]int{}})
		return
	}
	countries := map[string]int{}
	for _, s := range servers {
		countries[s.CountryShort]++
	}
	writeJSON(w, map[string]interface{}{
		"servers": servers, "fetched_at": at.Format("2006-01-02 15:04:05"), "via": via,
		"countries": countries, "count": len(servers), "protocols": Protocols,
		"labels": map[string]string{"l2tp": protocolLabel("l2tp"), "openvpn": protocolLabel("openvpn"), "sstp": protocolLabel("sstp"), "softether": protocolLabel("softether")},
	})
}

// apiProxy reads or saves the fetch proxy setting ("" = auto-detect).
func apiProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var q struct {
			Proxy string `json:"proxy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		curSettings.Proxy = strings.TrimSpace(q.Proxy)
		saveSettings(curSettings)
		conn.logf("Fetch proxy set to: %q (empty = auto-detect)", curSettings.Proxy)
	}
	writeJSON(w, map[string]interface{}{"proxy": curSettings.Proxy, "system_proxy": systemProxyURL()})
}

type connReq struct {
	Host     string `json:"host"`
	Protocol string `json:"protocol"`
}

func apiConnect(w http.ResponseWriter, r *http.Request) {
	var q connReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s, ok := store.find(q.Host)
	if !ok {
		http.Error(w, "server not found - refresh the list first", 404)
		return
	}
	conn.logf("Connecting to %s (%s, %s) via %s ...", s.HostName, s.IP, s.CountryLong, protocolLabel(q.Protocol))
	if err := connect(s, q.Protocol); err != nil {
		conn.logf("ERROR: %v", err)
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	conn.logf("Connect sequence finished for %s via %s.", s.HostName, protocolLabel(q.Protocol))
	writeJSON(w, map[string]interface{}{"ok": true})
}

func apiDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := disconnect(); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func apiDownload(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	kind := r.URL.Query().Get("kind") // ovpn | mobileconfig | softether
	s, ok := store.find(host)
	if !ok {
		http.Error(w, "server not found", 404)
		return
	}
	switch kind {
	case "ovpn":
		cfg, err := s.OVPNConfig()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\""+host+".ovpn\"")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(cfg))
	case "mobileconfig":
		w.Header().Set("Content-Disposition", "attachment; filename=\""+host+".mobileconfig\"")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(mobileConfig(s)))
	case "softether":
		w.Header().Set("Content-Disposition", "attachment; filename=\""+host+"-softether.txt\"")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(softEtherScript(s)))
	default:
		http.Error(w, "unknown kind", 400)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// fetchMode is the CLI smoke test: fetch, print summary + top JP servers, exit.
func fetchMode() int {
	servers, via, err := fetchServers()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		return 1
	}
	fmt.Printf("servers: %d (via %s)\n", len(servers), via)
	byCountry := map[string]int{}
	for _, s := range servers {
		byCountry[s.CountryShort]++
	}
	fmt.Printf("countries: %v\n", byCountry)
	sort.Slice(servers, func(i, j int) bool { return servers[i].Score > servers[j].Score })
	shown := 0
	for _, s := range servers {
		if s.CountryShort != "JP" {
			continue
		}
		fmt.Printf("JP %-24s %-15s ping=%dms speed=%.1fMbps sessions=%d uptime=%.1fd users=%d\n",
			s.HostName, s.IP, s.Ping, s.SpeedMbps, s.NumSessions, s.UptimeDays, s.TotalUsers)
		shown++
		if shown >= 5 {
			break
		}
	}
	if _, err := servers[0].OVPNConfig(); err != nil {
		fmt.Fprintln(os.Stderr, "ovpn:", err)
		return 1
	}
	fmt.Println("openvpn config decode: OK")
	return 0
}

func main() {
	port := flag.Int("port", 20124, "dashboard port")
	nobrowser := flag.Bool("nobrowser", false, "do not auto-open browser")
	proxyFlag := flag.String("proxy", "", "proxy for fetching the list, e.g. http://127.0.0.1:7890 or socks5://127.0.0.1:1080 (default: auto-detect)")
	doFetch := flag.Bool("fetch", false, "fetch server list, print summary, exit")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *proxyFlag != "" {
		curSettings.Proxy = *proxyFlag
	}
	if *showVer {
		fmt.Println("vpgui v" + version)
		return
	}
	if *doFetch {
		os.Exit(fetchMode())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(dashboardHTML))
	})
	mux.HandleFunc("/api/servers", apiServers)
	mux.HandleFunc("/api/proxy", apiProxy)
	mux.HandleFunc("/api/connect", apiConnect)
	mux.HandleFunc("/api/disconnect", apiDisconnect)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, conn.snapshot()) })
	mux.HandleFunc("/api/download", apiDownload)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	url := "http://" + addr
	fmt.Printf("vpgui v%s - VPN Gate quick launcher\nDashboard: %s\nWork dir: %s\n", version, url, workDir())
	if !*nobrowser {
		openBrowser(url)
	}
	http.Serve(ln, mux)
}
