# Changelog

## v0.1.0

First release.

- Scratch Link protocol over secure WebSocket (`/scratch/ble`), BLE only.
- Talks to BlueZ over D-Bus: no `bluepy`, no `glib`, no `setcap`.
- Several sessions share a single BlueZ scan.
- Closes the session when the device turns off or goes out of range.
- Generates its own local CA and server certificate on first run.
- Tested with a real LEGO WeDo 2.0 hub.
