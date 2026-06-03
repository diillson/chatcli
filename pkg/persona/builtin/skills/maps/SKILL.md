---
name: maps
description: Location intelligence — geocode a place, find nearby points of interest, compute routes/distance/ETA, reverse-geocode a coordinate, look up a timezone. Uses free open data (OpenStreetMap/Nominatim, Overpass, OSRM) with zero API keys. Use when asked "where is", "how far", "nearby", "directions to", "coordinates of", or given a location pin.
allowed-tools: ["@coder", "@webfetch", "Bash"]
triggers:
  - where is
  - how far
  - nearby
  - directions to
  - route to
  - coordinates of
  - distance between
  - what time is it in
  - onde fica
  - perto de
  - como chegar
  - rota para
  - distância entre
---

# Maps

Free, keyless location intelligence over open data — no Google Maps key, no signup.
All endpoints are plain HTTP, so `curl` (via `@coder exec`/Bash) or `@webfetch` works on any OS.

## Geocode (place name → coordinates)

Nominatim (set a real User-Agent — it's required by their policy):
```
curl -s "https://nominatim.openstreetmap.org/search?q=Eiffel+Tower&format=json&limit=1" \
  -H "User-Agent: chatcli/1.0"
```
Read `lat`/`lon`/`display_name` from the first result.

## Reverse geocode (coordinate → address)

A Telegram/WhatsApp **location pin** gives you lat/lon directly:
```
curl -s "https://nominatim.openstreetmap.org/reverse?lat=48.8584&lon=2.2945&format=json" \
  -H "User-Agent: chatcli/1.0"
```

## Nearby points of interest (Overpass)

Find cafes within 800 m of a point:
```
curl -s "https://overpass-api.de/api/interpreter" --data-urlencode \
  'data=[out:json];node["amenity"="cafe"](around:800,48.8584,2.2945);out 20;'
```
Swap `amenity=cafe` for `restaurant`, `pharmacy`, `atm`, `fuel`, `hospital`, etc.

## Route / distance / ETA (OSRM)

`lon,lat;lon,lat` order:
```
curl -s "https://router.project-osrm.org/route/v1/driving/2.2945,48.8584;2.3522,48.8566?overview=false"
```
Read `routes[0].distance` (meters) and `routes[0].duration` (seconds). Use `walking`/`cycling` profiles too.

## Timezone / local time

```
curl -s "https://timeapi.io/api/Time/current/coordinate?latitude=48.85&longitude=2.29"
```

## Rules

- Always send a `User-Agent` header to Nominatim/OSRM; they rate-limit anonymous traffic.
- Convert meters→km and seconds→min for the user; state the travel mode.
- These are public services — for heavy use, point the user at a self-hosted Nominatim/OSRM.
