# scratch-link-go

An **unofficial** replacement for
[Scratch Link](https://scratch.mit.edu/download/scratch-link) on **Linux**,
written in Go. One static binary, no Python, no `bluepy`, no `setcap`.

It lets Scratch 3 (and compatible editors such as TurboWarp) talk to Bluetooth
Low Energy devices — LEGO WeDo 2.0, micro:bit, LEGO BOOST — through BlueZ.

> Not affiliated with the Scratch Foundation, MIT or LEGO.

[Leer en español](README.es.md)

## Status

| Device | Status |
| --- | --- |
| LEGO WeDo 2.0 | Tested on real hardware |
| micro:bit, LEGO BOOST | Same protocol, **untested**. Reports welcome |
| LEGO EV3 and other classic Bluetooth devices | Not supported (BLE only) |

Linux only (BlueZ over D-Bus). x86-64 and ARM64 (Raspberry Pi) binaries are
published with each release.

## Requirements

- Linux with BlueZ running (`systemctl status bluetooth`) and a working adapter.
- Run it on the **host system**, not inside a container: Bluetooth is not
  available in most containers (Distrobox, Docker...).
- Your user must be allowed to use BlueZ. On a normal desktop session it already
  is; otherwise run `sudo usermod -aG bluetooth $USER` and log in again.

## Install

Download the binary for your architecture from the Releases page, then:

```sh
mkdir -p ~/.local/bin
install -m 755 scratch-link-go-linux-amd64 ~/.local/bin/scratch-link-go   # or ...-arm64
```

Or build from source (Go 1.25 or newer):

```sh
git clone https://github.com/almeida-aguilar/scratch-link-go
cd scratch-link-go
go mod tidy
go build -o scratch-link-go .
```

## Setup (one time)

Scratch looks for Scratch Link at `device-manager.scratch.mit.edu`, so point
that name at your own machine:

```sh
echo "127.0.0.1 device-manager.scratch.mit.edu" | sudo tee -a /etc/hosts
```

Start the program:

```sh
scratch-link-go
```

On first run it creates a certificate under `~/.local/share/scratch-link-go/`
and prints how to make your browser accept it. Pick **one**:

- **Accept the warning:** open <https://device-manager.scratch.mit.edu:20110/>
  in your browser and accept the security warning. You should then see a
  "scratch-link-go is running" message.
- **Import the CA** (Chromium/Chrome, shared NSS database):

  ```sh
  certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n scratch-link-go -i ~/.local/share/scratch-link-go/ca.pem
  ```

  (`certutil` comes from `libnss3-tools` on Debian/Ubuntu.) For Firefox, import
  `ca.pem` under
  *Settings → Privacy & Security → View Certificates → Authorities*.

Flatpak/Snap browsers have their own certificate stores: use the "accept the
warning" method inside that browser.

## Use

1. Run `scratch-link-go` and leave it running.
2. Turn the device on (WeDo 2.0: press the button until the LED blinks blue).
3. In Scratch, add the extension and click *Connect*.

Run it automatically at login with the systemd user service in
`contrib/scratch-link-go.service`.

## Options

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `127.0.0.1:20110` | Address to listen on |
| `-host` | `device-manager.scratch.mit.edu` | Name Scratch connects to |
| `-cert`, `-key` | generated | Use your own certificate and key (PEM) |
| `-data-dir` | `~/.local/share/scratch-link-go` | Where the generated certificate is stored |
| `-origins` | | Extra allowed web origins, comma-separated, or `*` for any |
| `-poll` | `1.5s` | How often to check the device is still connected |
| `-log` | `info` | `error`, `warn`, `info`, `debug`, `trace` |
| `-v` | | Same as `-log debug` |
| `-version` | | Print version |

By default only `scratch.mit.edu`, `turbowarp.org` and `localhost` pages may
connect, so other websites cannot drive your Bluetooth devices. Use `-origins`
to add more. `-log trace` also prints every sensor notification (very chatty).

## Troubleshooting

**"cannot use the Bluetooth adapter"** — BlueZ is not running or not reachable.
Check `systemctl status bluetooth`, `bluetoothctl show` (should say
`Powered: yes`), and `rfkill unblock bluetooth`. Inside a container, run it on
the host instead.

**`AccessDenied` on D-Bus** — add yourself to the `bluetooth` group (see
Requirements) and log in again.

**Scratch never finds the device** — make sure the device is advertising
(blinking LED) and not connected to another app or phone. Run with `-v` and
check that `device found` appears.

**The browser says the connection is not private / Scratch cannot connect** —
the certificate is not accepted yet. Open
<https://device-manager.scratch.mit.edu:20110/> and accept it, or import
`ca.pem`. If the page does not load at all, check the `/etc/hosts` line.

**"origin rejected"** in the log — the page you use is not on the allowed list;
add it with `-origins`.

**The hub blinks, turns off and restarts** — the session closed, which makes the
hub disconnect. Run with `-v` to see why.

**Already have a certificate from pyscrlink?** Reuse it with
`-cert ~/.local/share/pyscrlink/scratch-device-manager.cer -key ~/.local/share/pyscrlink/scratch-device-manager.key`
(your browser already trusts it).

## How it works

```text
Scratch (browser) --wss://device-manager.scratch.mit.edu:20110/scratch/ble--> scratch-link-go --D-Bus--> BlueZ --> device
```

Scratch speaks JSON-RPC 2.0 over the WebSocket (`discover`, `connect`, `read`,
`write`, `startNotifications`, `stopNotifications`, `getServices`); the program
translates each call to BlueZ and sends back `didDiscoverPeripheral` and
`characteristicDidChange` events.

| File | Role |
| --- | --- |
| `main.go` | Flags, logging, startup and shutdown |
| `server.go` | HTTPS/WebSocket endpoints, origin check |
| `session.go`, `rpc.go` | One Scratch session; JSON-RPC types and filters |
| `ble.go` | The `Backend` interface and UUID helpers |
| `ble_bluez.go` | The real backend (BlueZ via `tinygo.org/x/bluetooth`) |
| `scanmux.go` | Lets several sessions share BlueZ's single scan |
| `certs.go` | Local CA and server certificate generation |

## Development

```sh
go vet ./...
go test -race ./...
```

The protocol layer only talks to the `Backend` interface, so the tests run
without Bluetooth using a fake hub (`fake_test.go`).
`testdata/wedo2-session.log` holds the requests of a real Scratch ↔ WeDo 2.0
session, replayed in `TestReplayRealWeDoLog`. If you find a device that
misbehaves, run with `-v`, and a log like that makes an excellent bug report and
regression test.

## License

[0BSD](LICENSE): do whatever you want, no attribution required. Dependencies
([gorilla/websocket](https://github.com/gorilla/websocket),
[tinygo-org/bluetooth](https://github.com/tinygo-org/bluetooth),
[godbus](https://github.com/godbus/dbus)) are BSD-licensed; if you redistribute
binaries, keep their notices.
