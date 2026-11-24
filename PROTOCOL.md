# Q8 ⇄ Relay protocol, version 1

Every response carries `Q8-Relay-Protocol: 1`. A Q8 that needs a newer
protocol tells the user to update Relay; Relay's page shows its own.

Q8 always connects out to Relay: Q8 never listens, so the Mac needs no open
port or firewall exception.

## Finding Relay

Bonjour `_q8._tcp`, TXT `id=<relay id>` and `protocol=1`. From a container,
mDNS reaches the network only with `network_mode: host` (or macvlan), so Q8
also takes a typed `host:port` (default port 8484).

## `GET /v1/info` (no key)

```json
{"name": "Q8 Relay", "id": "3f9c0a1b2c3d4e5f", "version": "0.1.0",
 "protocol": 1, "mode": "connect", "apps": ["radarr", "sonarr"]}
```

`mode` is `connect` ("Connect what you have": read-only). `focused` is
reserved for Q8 focused.

## `POST /v1/pair` (no key)

Body `{"code": "1234-5678", "name": "Living Room Mac"}`. The code is on
Relay's page and in its log; it changes when used and every 15 minutes. Five
wrong codes a minute, then `429`.

`200` → `{"key": "<64 hex>", "clientId": "…", "relayId": "…"}`. Q8 keeps the
key in its Keychain. `403` wrong or expired code.

Every request below sends `Authorization: Bearer <key>`; without a paired key
it's `401`.

## `GET /v1/events` — Server-Sent Events

Resumes after `Last-Event-ID` (sent by an EventSource on reconnect) or
`?since=<seq>`: first everything Relay still remembers after that number
(the newest 500), then each new arrival as it happens. A `: alive` comment
every 20 s.

```
id: 12
event: arrival
data: {"seq":12,"id":"radarr-42","app":"radarr","kind":"movie",
       "fileName":"Dune (2021).mkv","size":8123456789,
       "importedAt":"2026-11-24T11:02:03Z","fileUrl":"/v1/files/radarr-42",
       "title":"Dune","year":2021,"tmdbId":438631}
```

| field | |
|---|---|
| `seq` | Order of announcement; Q8 keeps the last one it handled. |
| `id` | `<app>-<file id>`: one file, one arrival. |
| `kind` | `movie`, `episode` or `track`. |
| `title`, `year`, `tmdbId` | The film; for an episode the show (`tvdbId` too). |
| `season`, `episode`, `episodeTvdbId`, `episodeTitle` | Episodes. |
| `artist`, `albumId` | Tracks: Lidarr imports per track, so Q8 groups by `albumId`. |

An arrival is sent only once the file is whole: the arr's size, the same on
two checks running.

## `GET /v1/arrivals?since=<seq>`

The same arrivals as JSON, for a Q8 that prefers to ask:
`{"latest": 12, "arrivals": [ … ]}`.

## Q8 focused

`GET /v1/focused` → `{"enabled": false, "apps": [{"app": "radarr", "ready":
false, "problem": "Set a Recycle Bin folder…"}]}`. An app is ready with a
Recycle Bin set and no other Relay's `q8-relay-*` tag.

`POST /v1/focused` with `{"enabled": true}` takes every app or none (`409`
with the report and `error` when one isn't ready), adding the tag;
`{"enabled": false}` removes it. `/v1/info`'s `mode` says `focused` while on.

`POST /v1/confirm` with `{"ids": ["radarr-42", …]}`: Q8 has these, converted
and checked. For each, Relay checks the arr's file is still the one announced
(same size), unmonitors it (Radarr's film, Sonarr's episodes on that file,
Lidarr's album), then deletes the file through the arr (into its Recycle
Bin). `{"outcomes": [{"id": "radarr-42"}, {"id": "…", "error": "…"}]}`.
Asking twice is fine. `409` while Q8 focused is off: nothing is deleted.

## `GET /v1/files/{id}`

The file, by arrival id only, never by path. `Range` is honoured, so a
download cut short resumes. `404` an id Relay never announced (or has
forgotten), `410` a file that's gone (the arr upgraded or deleted it).
