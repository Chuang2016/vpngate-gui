package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// VPN Gate public relay credentials (same for every server, per official docs).
const (
	vpnUser = "vpn"
	vpnPass = "vpn"
	vpnPSK  = "vpn" // L2TP/IPsec pre-shared key
)

// Protocols supported by VPN Gate relay servers.
var Protocols = []string{"l2tp", "openvpn", "sstp", "softether"}

func protocolLabel(p string) string {
	switch p {
	case "l2tp":
		return "L2TP/IPsec"
	case "openvpn":
		return "OpenVPN"
	case "sstp":
		return "MS-SSTP"
	case "softether":
		return "SoftEther (SSL-VPN)"
	}
	return p
}

type connState struct {
	mu       sync.Mutex
	Active   bool     `json:"active"`
	Host     string   `json:"host"`
	IP       string   `json:"ip"`
	Protocol string   `json:"protocol"`
	Started  string   `json:"started"`
	Log      []string `json:"log"`
	cmd      *exec.Cmd
}

var conn = &connState{}

func (c *connState) logf(format string, a ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf(format, a...)
	c.Log = append(c.Log, line)
	if len(c.Log) > 200 {
		c.Log = c.Log[len(c.Log)-200:]
	}
}

func (c *connState) snapshot() map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]interface{}{
		"active": c.Active, "host": c.Host, "ip": c.IP,
		"protocol": c.Protocol, "started": c.Started, "log": c.Log,
	}
}

func workDir() string {
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, ".vpngate-gui")
	os.MkdirAll(d, 0700)
	return d
}

func findBinary(names []string, extraPaths []string) string {
	for _, p := range extraPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// connect attempts to bring up the chosen protocol for server s.
// It returns human-readable steps/results for the UI log. Native OS VPN
// (L2TP/SSTP on Windows) is created via PowerShell; OpenVPN/SoftEther/SSTP
// on macOS/Linux run the matching open-source client when installed, and
// otherwise fall back to a generated config/profile the user can import.
func connect(s Server, proto string) error {
	conn.mu.Lock()
	if conn.Active {
		conn.mu.Unlock()
		return fmt.Errorf("already connected to %s - disconnect first", conn.Host)
	}
	conn.mu.Unlock()
	switch proto {
	case "openvpn":
		return connectOpenVPN(s)
	case "l2tp":
		return connectL2TP(s)
	case "sstp":
		return connectSSTP(s)
	case "softether":
		return connectSoftEther(s)
	}
	return fmt.Errorf("unknown protocol %q", proto)
}

func markActive(s Server, proto string, cmd *exec.Cmd) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.Active, conn.Host, conn.IP, conn.Protocol = true, s.HostName, s.IP, proto
	conn.Started = timeNow()
	conn.cmd = cmd
}

func timeNow() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func disconnect() error {
	conn.mu.Lock()
	cmd, proto, host, active := conn.cmd, conn.Protocol, conn.Host, conn.Active
	conn.mu.Unlock()
	if !active {
		return fmt.Errorf("not connected")
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
	if runtime.GOOS == "windows" && (proto == "l2tp" || proto == "sstp") {
		name := "VPNGate-" + host
		exec.Command("rasdial", name, "/disconnect").Run()
		exec.Command("powershell", "-NoProfile", "-Command",
			"Remove-VpnConnection -Name '"+name+"' -Force -ErrorAction SilentlyContinue").Run()
	}
	conn.mu.Lock()
	conn.Active, conn.cmd = false, nil
	conn.mu.Unlock()
	conn.logf("Disconnected from %s", host)
	return nil
}

func runLogged(name string, args ...string) error {
	c := exec.Command(name, args...)
	out, err := c.CombinedOutput()
	conn.logf("$ %s %s", name, strings.Join(args, " "))
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			conn.logf("  %s", l)
		}
	}
	return err
}

func connectOpenVPN(s Server) error {
	cfg, err := s.OVPNConfig()
	if err != nil {
		return err
	}
	path := filepath.Join(workDir(), s.HostName+".ovpn")
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		return err
	}
	conn.logf("OpenVPN config saved: %s", path)
	bin := findBinary([]string{"openvpn", "openvpn.exe"}, []string{
		`C:\Program Files\OpenVPN\bin\openvpn.exe`,
		"/opt/homebrew/bin/openvpn", "/usr/local/bin/openvpn", "/usr/sbin/openvpn",
	})
	if bin == "" {
		conn.logf("No openvpn binary found. Import the downloaded .ovpn file into OpenVPN Connect / Tunnelblick, or install OpenVPN and retry.")
		markActive(s, "openvpn", nil)
		return nil
	}
	conn.logf("Launching %s (admin/root may be required)...", bin)
	c := exec.Command(bin, "--config", path)
	if err := c.Start(); err != nil {
		return fmt.Errorf("openvpn failed to start: %v (try running vpgui as Administrator/root)", err)
	}
	markActive(s, "openvpn", c)
	go func() { c.Wait(); conn.logf("openvpn process exited") }()
	return nil
}

func connectL2TP(s Server) error {
	switch runtime.GOOS {
	case "windows":
		name := "VPNGate-" + s.HostName
		ps := "Add-VpnConnection -Name '" + name + "' -ServerAddress '" + s.IP +
			"' -TunnelType L2tp -L2tpPsk '" + vpnPSK + "' -AuthenticationMethod MSChapv2,Pap" +
			" -RememberCredential -Force -ErrorAction Stop"
		if err := runLogged("powershell", "-NoProfile", "-Command", ps); err != nil {
			return fmt.Errorf("creating VPN connection failed: %v (run vpgui as Administrator)", err)
		}
		if err := runLogged("rasdial", name, vpnUser, vpnPass); err != nil {
			return fmt.Errorf("rasdial failed: %v", err)
		}
		markActive(s, "l2tp", nil)
		return nil
	case "darwin":
		p := mobileConfigPath(s)
		conn.logf("macOS: open the downloaded profile %s, then in System Settings > VPN connect to VPNGate-%s (user %s / pass %s, secret %s).", p, s.HostName, vpnUser, vpnPass, vpnPSK)
		exec.Command("open", p).Start()
		markActive(s, "l2tp", nil)
		return nil
	default: // linux
		conn.logf("Linux L2TP: server %s, user %s / pass %s, IPsec PSK %s. Use NetworkManager (network-manager-l2tp) or xl2tpd+strongSwan with these values.", s.IP, vpnUser, vpnPass, vpnPSK)
		if _, err := exec.LookPath("nmcli"); err == nil {
			conn.logf("nmcli found - install the network-manager-l2tp plugin, then add an L2TP connection with the values above.")
		}
		markActive(s, "l2tp", nil)
		return nil
	}
}

func connectSSTP(s Server) error {
	switch runtime.GOOS {
	case "windows":
		name := "VPNGate-" + s.HostName
		ps := "Add-VpnConnection -Name '" + name + "' -ServerAddress 'https://" + s.IP +
			"' -TunnelType Sstp -AuthenticationMethod MSChapv2,Pap -RememberCredential -Force -ErrorAction Stop"
		if err := runLogged("powershell", "-NoProfile", "-Command", ps); err != nil {
			return fmt.Errorf("creating VPN connection failed: %v (run vpgui as Administrator)", err)
		}
		if err := runLogged("rasdial", name, vpnUser, vpnPass); err != nil {
			return fmt.Errorf("rasdial failed: %v", err)
		}
		markActive(s, "sstp", nil)
		return nil
	default:
		bin := findBinary([]string{"sstpc"}, []string{"/usr/local/bin/sstpc", "/opt/homebrew/bin/sstpc"})
		if bin == "" {
			conn.logf("No sstpc client found. SSTP endpoint: https://%s:443 (user %s / pass %s). Install sstp-client and retry, or use L2TP/OpenVPN on this OS.", s.IP, vpnUser, vpnPass)
			markActive(s, "sstp", nil)
			return nil
		}
		conn.logf("Launching sstpc to %s ...", s.IP)
		c := exec.Command(bin, "--log-stderr", "--cert-warn", "--user", vpnUser, "--password", vpnPass, s.IP, "usepeerdns", "require-mschap-v2", "noauth", "lock", "noipdefault", "defaultroute")
		if err := c.Start(); err != nil {
			return fmt.Errorf("sstpc failed: %v (root may be required)", err)
		}
		markActive(s, "sstp", c)
		go func() { c.Wait(); conn.logf("sstpc process exited") }()
		return nil
	}
}

func connectSoftEther(s Server) error {
	bin := findBinary([]string{"vpncmd", "vpncmd.exe"}, []string{
		`C:\Program Files\SoftEther VPN Client\vpncmd.exe`,
		"/usr/local/bin/vpncmd", "/opt/homebrew/bin/vpncmd",
	})
	if bin == "" {
		conn.logf("SoftEther VPN Client not found. Install it (softether.org), create a connection: host %s:443, HUB 'VPNGATE', user %s / pass %s - or use the generated vpncmd script in the work folder.", s.IP, vpnUser, vpnPass)
		script := softEtherScript(s)
		p := filepath.Join(workDir(), s.HostName+"-softether.txt")
		os.WriteFile(p, []byte(script), 0600)
		conn.logf("vpncmd script saved: %s", p)
		markActive(s, "softether", nil)
		return nil
	}
	acct := "VPNGate-" + s.HostName
	cmds := [][]string{
		{"localhost", "/CLIENT", "/CMD", "AccountCreate", acct, "/SERVER:" + s.IP + ":443", "/HUB:VPNGATE", "/USERNAME:" + vpnUser, "/NICNAME:VPN"},
		{"localhost", "/CLIENT", "/CMD", "AccountPasswordSet", acct, "/PASSWORD:" + vpnPass, "/TYPE:standard"},
		{"localhost", "/CLIENT", "/CMD", "AccountConnect", acct},
	}
	for _, a := range cmds {
		if err := runLogged(bin, a...); err != nil {
			conn.logf("vpncmd step failed (is the SoftEther client service running?): %v", err)
		}
	}
	markActive(s, "softether", nil)
	return nil
}

// softEtherScript returns a vpncmd batch usable on any OS with the client.
func softEtherScript(s Server) string {
	acct := "VPNGate-" + s.HostName
	return "vpncmd localhost /CLIENT /CMD AccountCreate " + acct + " /SERVER:" + s.IP + ":443 /HUB:VPNGATE /USERNAME:" + vpnUser + " /NICNAME:VPN\r\n" +
		"vpncmd localhost /CLIENT /CMD AccountPasswordSet " + acct + " /PASSWORD:" + vpnPass + " /TYPE:standard\r\n" +
		"vpncmd localhost /CLIENT /CMD AccountConnect " + acct + "\r\n"
}

// mobileConfigPath writes a macOS L2TP configuration profile and returns its path.
func mobileConfigPath(s Server) string {
	p := filepath.Join(workDir(), s.HostName+".mobileconfig")
	os.WriteFile(p, []byte(mobileConfig(s)), 0600)
	return p
}

// mobileConfig builds an Apple Configuration Profile for L2TP over IPsec
// (shared secret = "vpn", user = "vpn", password = "vpn").
func mobileConfig(s Server) string {
	name := "VPNGate-" + s.HostName
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>PayloadContent</key><array><dict>
<key>PayloadType</key><string>com.apple.vpn.managed</string>
<key>PayloadVersion</key><integer>1</integer>
<key>PayloadIdentifier</key><string>com.vpngate-gui.` + s.HostName + `.vpn</string>
<key>PayloadUUID</key><string>3F2504E0-4F89-41D3-9A0C-0305E82C3301</string>
<key>PayloadDisplayName</key><string>` + name + `</string>
<key>UserDefinedName</key><string>` + name + `</string>
<key>VPNType</key><string>L2TP</string>
<key>L2TP</key><dict>
<key>ServerAddress</key><string>` + s.IP + `</string>
<key>SharedSecret</key><string>` + vpnPSK + `</string>
<key>CommRemoteAddress</key><string>` + s.IP + `</string>
</dict>
<key>PPP</key><dict>
<key>AuthName</key><string>` + vpnUser + `</string>
<key>AuthPassword</key><string>` + vpnPass + `</string>
<key>CommRemoteAddress</key><string>` + s.IP + `</string>
</dict>
</dict></array>
<key>PayloadDisplayName</key><string>` + name + `</string>
<key>PayloadIdentifier</key><string>com.vpngate-gui.` + s.HostName + `</string>
<key>PayloadType</key><string>Configuration</string>
<key>PayloadUUID</key><string>3F2504E0-4F89-41D3-9A0C-0305E82C3300</string>
<key>PayloadVersion</key><integer>1</integer>
</dict></plist>`
}
