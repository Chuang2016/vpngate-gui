# vpgui — VPN Gate Quick Launcher

A single-binary GUI for [VPN Gate](https://www.vpngate.net) (the University of
Tsukuba academic relay project). It fetches the live relay list from the
official VPN Gate API and lets you filter, sort and connect in one click.

No dependencies: pure Go (standard library only), ~7 MB binary, embedded web
dashboard at `http://127.0.0.1:20124`.

## Features

- **Live server list** from the official API (`/api/iphone/`), cached 10 min
- **Filters**: country/region, search (host/IP/operator), min speed (Mbps),
  max ping, max online sessions, min uptime (days)
- **Sorting**: score, speed, ping, online sessions, uptime days, total users
  (click table headers or use the Sort dropdown)
- **Quick pick**: one button connects the best server matching your filters
- **Four protocols, one click per server**:
  | Protocol | How it connects |
  |---|---|
  | L2TP/IPsec | Windows: native connection via PowerShell + `rasdial`. macOS: generates & opens a `.mobileconfig` L2TP profile. Linux: NetworkManager/xl2tpd values in the log |
  | OpenVPN | Decodes the embedded `.ovpn`; launches `openvpn` if installed, else use the per-row `.ovpn` download (OpenVPN Connect / Tunnelblick) |
  | MS-SSTP | Windows: native SSTP connection. macOS/Linux: `sstpc` (sstp-client) if installed |
  | SoftEther (SSL-VPN) | `vpncmd` account create + connect if SoftEther VPN Client is installed; otherwise a ready vpncmd script is generated |
- All relays share the public credentials: user `vpn` / password `vpn`,
  L2TP IPsec pre-shared key `vpn`
- Connection status + log console in the dashboard, Disconnect button

## Download

Prebuilt binaries are on the
[Releases](https://github.com/Chuang2016/vpngate-gui/releases) page:

- `vpgui.exe` — Windows x64
- `vpgui-darwin-arm64` — macOS Apple Silicon
- `vpgui-darwin-amd64` — macOS Intel
- `vpgui-linux-amd64` / `vpgui-linux-arm64`

macOS first run: the binary is unsigned — right-click → Open once.
Windows native L2TP/SSTP creation works best when run as Administrator.

## Usage

```sh
./vpgui              # opens the dashboard in your browser
./vpgui -port 20124  # custom port
./vpgui -nobrowser   # headless
./vpgui -fetch       # CLI smoke test: fetch list, print summary, exit
./vpgui -version
```

Default filter is Japan (JP), the largest and fastest relay pool; switch to
"All countries" or any region in the Country dropdown.

## Build

```sh
./build.sh           # cross-compiles Windows / macOS / Linux into dist/
```

## Notes

- VPN Gate relays are run by volunteers for academic use; availability and
  speed change constantly — refresh the list before connecting.
- Per-server downloads: `.ovpn` (OpenVPN), `.mobileconfig` (macOS L2TP),
  SoftEther vpncmd script.
- Work files (downloaded configs, profiles) are stored in `~/.vpngate-gui/`.
