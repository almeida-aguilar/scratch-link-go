# scratch-link-go

Reemplazo **no oficial** de [Scratch Link](https://scratch.mit.edu/download/scratch-link) para **Linux**, escrito en Go. Un solo binario estático, sin Python, sin `bluepy`, sin `setcap`.

Permite que Scratch 3 (y editores compatibles como TurboWarp) hablen con dispositivos Bluetooth de bajo consumo (BLE) como LEGO WeDo 2.0, micro:bit o LEGO BOOST a través de BlueZ.

> Sin relación con la Fundación Scratch, el MIT ni LEGO.

[Read in English](README.md)

## Estado

| Dispositivo | Estado |
| --- | --- |
| LEGO WeDo 2.0 | Probado con hardware real |
| micro:bit, LEGO BOOST | Mismo protocolo, **sin probar**. Se agradecen reportes |
| LEGO EV3 y Bluetooth clásico | No soportado (solo BLE) |

Solo Linux (BlueZ por D-Bus). Cada versión publica binarios para x86-64 y ARM64 (Raspberry Pi).

## Requisitos

- Linux con BlueZ activo (`systemctl status bluetooth`) y un adaptador que funcione.
- Ejecútalo en el **sistema anfitrión**, no dentro de un contenedor: el Bluetooth no está disponible en la mayoría (Distrobox, Docker...).
- Tu usuario debe poder usar BlueZ. En una sesión de escritorio normal ya puede; si no, `sudo usermod -aG bluetooth $USER` y vuelve a iniciar sesión.

## Instalación

Descarga el binario de tu arquitectura desde la página de Releases y:

```sh
mkdir -p ~/.local/bin
install -m 755 scratch-link-go-linux-amd64 ~/.local/bin/scratch-link-go   # o ...-arm64
```

O compila desde el código (Go 1.25 o superior):

```sh
git clone https://github.com/almeida-aguilar/scratch-link-go
cd scratch-link-go
go mod tidy
go build -o scratch-link-go .
```

## Configuración (una sola vez)

Scratch busca Scratch Link en `device-manager.scratch.mit.edu`, así que hay que apuntar ese nombre a tu propia máquina:

```sh
echo "127.0.0.1 device-manager.scratch.mit.edu" | sudo tee -a /etc/hosts
```

Arranca el programa:

```sh
scratch-link-go
```

La primera vez crea un certificado en `~/.local/share/scratch-link-go/` y te dice cómo hacer que el navegador lo acepte. Elige **una** opción:

- **Aceptar el aviso:** abre <https://device-manager.scratch.mit.edu:20110/> en el navegador y acepta la advertencia de seguridad. Deberías ver el mensaje "scratch-link-go is running".
- **Importar la CA** (Chromium/Chrome, base NSS compartida):
  ```sh
  certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n scratch-link-go -i ~/.local/share/scratch-link-go/ca.pem
  ```
  (`certutil` viene en `libnss3-tools` en Debian/Ubuntu.) En Firefox, importa `ca.pem` en *Ajustes → Privacidad y seguridad → Ver certificados → Autoridades*.

Los navegadores Flatpak/Snap tienen su propio almacén de certificados: usa el método de "aceptar el aviso" dentro de ese navegador.

## Uso

1. Ejecuta `scratch-link-go` y déjalo abierto.
2. Enciende el dispositivo (WeDo 2.0: pulsa el botón hasta que el LED parpadee en azul).
3. En Scratch, añade la extensión y pulsa *Conectar*.

Para que arranque solo al iniciar sesión, usa el servicio de usuario de systemd de `contrib/scratch-link-go.service`.

## Opciones

| Opción | Por defecto | Significado |
| --- | --- | --- |
| `-addr` | `127.0.0.1:20110` | Dirección donde escucha |
| `-host` | `device-manager.scratch.mit.edu` | Nombre al que se conecta Scratch |
| `-cert`, `-key` | generados | Usar tu propio certificado y clave (PEM) |
| `-data-dir` | `~/.local/share/scratch-link-go` | Dónde se guarda el certificado generado |
| `-origins` | | Orígenes web extra permitidos, separados por comas, o `*` para cualquiera |
| `-poll` | `1.5s` | Cada cuánto se comprueba que el dispositivo sigue conectado |
| `-log` | `info` | `error`, `warn`, `info`, `debug`, `trace` |
| `-v` | | Igual que `-log debug` |
| `-version` | | Muestra la versión |

Por defecto solo pueden conectarse páginas de `scratch.mit.edu`, `turbowarp.org` y `localhost`, para que otras webs no puedan controlar tus dispositivos Bluetooth. Usa `-origins` para añadir más. `-log trace` imprime además cada notificación de sensores (muy ruidoso).

## Solución de problemas

**"cannot use the Bluetooth adapter"**: BlueZ no está activo o no es accesible. Revisa `systemctl status bluetooth`, `bluetoothctl show` (debe decir `Powered: yes`) y `rfkill unblock bluetooth`. Dentro de un contenedor, ejecútalo en el anfitrión.

**`AccessDenied` en D-Bus**: añade tu usuario al grupo `bluetooth` (ver Requisitos) y reinicia la sesión.

**Scratch no encuentra el dispositivo**: comprueba que está anunciándose (LED parpadeando) y que no está conectado a otra app o móvil. Ejecuta con `-v` y mira si aparece `device found`.

**El navegador dice que la conexión no es privada / Scratch no conecta**: el certificado aún no está aceptado. Abre <https://device-manager.scratch.mit.edu:20110/> y acéptalo, o importa `ca.pem`. Si la página no carga, revisa la línea de `/etc/hosts`.

**"origin rejected" en el log**: la página que usas no está en la lista permitida; añádela con `-origins`.

**El hub parpadea, se apaga y se reinicia**: la sesión se cerró, lo que hace que el hub se desconecte. Ejecuta con `-v` para ver el motivo.

**¿Ya tienes un certificado de pyscrlink?** Reutilízalo con `-cert ~/.local/share/pyscrlink/scratch-device-manager.cer -key ~/.local/share/pyscrlink/scratch-device-manager.key` (tu navegador ya confía en él).

## Cómo funciona

```
Scratch (navegador) --wss://device-manager.scratch.mit.edu:20110/scratch/ble--> scratch-link-go --D-Bus--> BlueZ --> dispositivo
```

Scratch habla JSON-RPC 2.0 por el WebSocket (`discover`, `connect`, `read`, `write`, `startNotifications`, `stopNotifications`, `getServices`); el programa traduce cada llamada a BlueZ y devuelve los eventos `didDiscoverPeripheral` y `characteristicDidChange`.

## Desarrollo

```sh
go vet ./...
go test -race ./...
```

La capa de protocolo solo habla con la interfaz `Backend`, así que los tests corren sin Bluetooth usando un hub falso (`fake_test.go`). `testdata/wedo2-session.log` contiene las peticiones de una sesión real de Scratch con un WeDo 2.0, que `TestReplayRealWeDoLog` reproduce. Si encuentras un dispositivo que falla, ejecuta con `-v`: un log así es un reporte de error excelente y un buen test de regresión.

## Licencia

[0BSD](LICENSE): haz lo que quieras, sin necesidad de atribución. Las dependencias ([gorilla/websocket](https://github.com/gorilla/websocket), [tinygo-org/bluetooth](https://github.com/tinygo-org/bluetooth), [godbus](https://github.com/godbus/dbus)) tienen licencia BSD; si redistribuyes binarios, conserva sus avisos.
