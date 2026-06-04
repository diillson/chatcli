---
name: hue-lights
description: Control Philips Hue smart lights over the LOCAL network (Hue Bridge REST API) — turn lights on/off, set brightness/color, activate scenes, list rooms. Keyless (local bridge token, no cloud). Use when asked to "turn on/off the lights", "dim the lights", "set the lights to blue", "lights scene".
allowed-tools: ["@coder", "Bash", "@webfetch"]
triggers:
  - turn on the lights
  - turn off the lights
  - dim the lights
  - set the lights
  - lights to
  - light scene
  - acender a luz
  - apagar a luz
  - diminuir a luz
  - luzes
---

# Philips Hue (local control)

Control Hue lights via the **local Bridge REST API** — no cloud account, no API key. Auth is a
locally-generated username/application key bound to your LAN.

## One-time setup

1. Find the bridge IP: `curl -s https://discovery.meethue.com` (returns LAN IP), or check the router.
2. Generate a local user: press the bridge's link button, then within 30 s:
   ```
   curl -s -X POST http://<BRIDGE_IP>/api -d '{"devicetype":"chatcli#host"}'
   ```
   Save the returned `username` as `HUE_BRIDGE_IP` + `HUE_USER` (env vars).

(If `openhue` CLI is installed, `openhue set ...` / `openhue get lights` wraps all of this — detect
`command -v openhue` and prefer it.)

## List lights / rooms

```
curl -s "http://$HUE_BRIDGE_IP/api/$HUE_USER/lights"
curl -s "http://$HUE_BRIDGE_IP/api/$HUE_USER/groups"   # rooms/zones
```

## Control

```
# On + warm white, full brightness (light id 3)
curl -s -X PUT "http://$HUE_BRIDGE_IP/api/$HUE_USER/lights/3/state" -d '{"on":true,"bri":254,"ct":366}'
# Off
curl -s -X PUT "http://$HUE_BRIDGE_IP/api/$HUE_USER/lights/3/state" -d '{"on":false}'
# Color (hue 0-65535, sat 0-254): blue ≈ hue 46920
curl -s -X PUT "http://$HUE_BRIDGE_IP/api/$HUE_USER/lights/3/state" -d '{"on":true,"hue":46920,"sat":254}'
# Whole room (group 1)
curl -s -X PUT "http://$HUE_BRIDGE_IP/api/$HUE_USER/groups/1/action" -d '{"on":true,"bri":150}'
```

## Rules

- It's LAN-only — the host must be on the same network as the bridge.
- Resolve names→ids first (list lights/groups) so "the kitchen lights" maps to the right group.
- `bri` 1–254, `ct` (warm↔cool) ~153–500, `hue`/`sat` for color. Confirm the target before changing.
