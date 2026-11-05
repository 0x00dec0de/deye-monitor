# Deye UPS monitor

Small Go web server that polls a Deye single-phase hybrid inverter through its
Solarman LSW-3 WiFi logger and shows a UPS-style dashboard: current power source
(grid / battery / solar), load, battery state of charge, voltage, current,
temperature, estimated runtime, energy counters, a power/SOC history chart and an
outage event log.

No external dependencies: the Solarman V5 framing and Modbus RTU are implemented
in `solarman.go`, register decoding in `deye.go`, the page is embedded from `web/`.

## Run

```sh
go build -o deye-monitor .
./deye-monitor -addr :8080 -inverter 192.168.1.232:8899 -serial 2983308769
```

Open http://localhost:8080. JSON endpoints:

- `GET /api/status` – latest decoded reading, poll error state and events
- `GET /api/history?range=6h` – thinned samples for the charts (up to `-history`, default 48h)

Flags (or environment variables): `-addr` (`LISTEN_ADDR`), `-inverter`
(`INVERTER_ADDR`), `-serial` (`LOGGER_SERIAL`, the logger stick serial, not the
inverter serial), `-slave` (`MODBUS_SLAVE`), `-interval` (`POLL_INTERVAL`),
`-history` (`HISTORY`), `-data` (`DATA_FILE`, history/events persisted here every minute).

## Install as a service

It runs as a systemd *user* service so desktop notifications reach your session:

```sh
go build -o deye-monitor .
cp deploy/deye-monitor.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now deye-monitor
loginctl enable-linger $USER   # keep it running at boot, before login
```

Logs: `journalctl --user -u deye-monitor -f`.

## Notifications

On grid lost, grid restored and low battery the monitor runs the `-notify-cmd`
shell command (`NOTIFY_CMD` env). The default sends a desktop notification:

```sh
notify-send -a "Deye UPS" -u "$EVENT_URGENCY" -i battery "Deye: $EVENT_MESSAGE" "$EVENT_TIME"
```

Grid lost and low battery use `critical` urgency, so the popup stays until
dismissed. The command gets `EVENT_KIND`, `EVENT_MESSAGE`, `EVENT_URGENCY` and
`EVENT_TIME` in its environment, so it can be swapped for a Telegram, email or
any other command. Set it to `''` to disable.

## Notes

- Register map is the Deye SUN-xK-SG0xLP1 (single-phase hybrid) map, holding
  registers 0–260. Three-phase models use 500+ and would need a different decoder.
- Runtime estimate = usable energy above the inverter's shutdown SOC, with a
  51.2 V nominal battery, divided by the present load (or battery discharge power
  when already on battery). It is an estimate, not a BMS value.
- Grid presence comes from register 194 plus a grid-voltage sanity check; a
  grid-lost/restored transition is logged as an event.
