# Q8 Relay

Relay connects your Radarr, Sonarr and Lidarr to [Q8](https://q-8.app), a Mac
app that names, tags and converts your films, TV, music, audiobooks and books.

Relay checks your arrs every 10 seconds. Once an imported file is complete,
it tells Q8 at once, over a connection Q8 keeps open, usually within 20
seconds of the import. Q8 downloads the file from Relay, converts it, and
puts it into the TV app or your library folder. No network share needed.
Without Relay, Q8 asks your arrs itself every five minutes and reads the
files over a share.

- Runs beside your arrs as one small container (amd64 and arm64)
- Read-only by default: it changes nothing in your arrs
- MIT licensed

```
ghcr.io/q8-app/q8-relay:latest
```

## Quick start

Add Relay to the compose file that runs your arrs:

```yaml
  q8-relay:
    image: ghcr.io/q8-app/q8-relay:0.1
    container_name: q8-relay
    user: "1000:1000"            # your arrs' PUID:PGID
    environment:
      RADARR_URL: http://radarr:7878
      RADARR_API_KEY: ${RADARR_API_KEY}
      SONARR_URL: http://sonarr:8989
      SONARR_API_KEY: ${SONARR_API_KEY}
    volumes:
      - ./q8-relay:/config
      - /path/to/data:/data:ro   # the same volume, at the same path, as your arrs
    ports:
      - "8484:8484"
    restart: unless-stopped
```

1. Run `docker compose up -d q8-relay`.
2. Open `http://<server>:8484`. The page shows a pairing code, and so does
   `docker logs q8-relay`.
3. In Q8, open **Media Server**, click **+**, then **Connect to Relay…**.
   Select Relay, or type its address, and enter the code.

A full example with comments is in [compose.yaml](compose.yaml).

### The one thing that must match

Relay reads files at the paths your arrs report, so mount the same folder at
the same path. If your stack mounts it somewhere else, map it:

```
RELAY_PATH_MAP=/data=/mnt/storage,/downloads=/mnt/downloads
```

### Let Q8 find Relay by itself

Bonjour needs `network_mode: host` (Linux) instead of `ports:`. Without it,
type `server:8484` in Q8.

## How it works

Relay starts in **Connect what you have** mode:

- It reads your arrs' import history every 10 seconds
  (`RELAY_POLL_INTERVAL`), with GET requests only. It adds no webhook, so
  nothing in your arrs needs to reach it.
- It waits until each imported file is complete (the arr's size, on two
  checks running), then announces it to the Q8s paired with it at once.
- It serves that file, and only that file, over HTTP. Interrupted downloads
  resume.
- It leaves your arrs' settings and files exactly as they are.

On its first run Relay ignores everything already imported. What your arrs
had before is your library, not news, so only new imports are announced.

The arrs' API keys stay in Relay. Q8 holds only its own pairing key, kept in
the Mac's Keychain. Relay stores just a hash of that key. Q8 always connects
out to Relay, so the Mac needs no open port.

### Q8 focused (optional, switched on from Q8)

Read this before turning it on. Once Q8 has converted a file and checked it
(as long as the original, with its video and audio), Relay **unmonitors it in
its arr and deletes the arr's copy** into the arr's Recycle Bin, so it isn't
downloaded again. A torrent keeps seeding through its hard link. A file Q8
couldn't check stays in its arr.

- Every arr needs a **Recycle Bin** folder (Settings › Media Management).
  Without one, Q8 focused won't turn on.
- The only setting Relay adds is a tag, `q8-relay-<id>`, marking the arr as
  this Relay's. One arr, one Relay: a second Relay is refused.
- **Undo Q8 Focused** in Q8 removes the tag, and nothing more is deleted.

**Firewall:** with `network_mode: host`, Relay sits behind the host's
firewall, unlike containers with published ports (Docker bypasses UFW for
those). Allow port 8484 from the network your Macs are on, for example
`sudo ufw allow from 192.168.1.0/24 to any port 8484 proto tcp`. Bonjour
needs no rule: Ubuntu's UFW accepts mDNS by default. If the Macs are on
another network or VLAN than the server, Bonjour only crosses with your
router's mDNS reflector on; otherwise type the address in Q8.

## Settings

| Variable | Default | |
|---|---|---|
| `RADARR_URL`, `RADARR_API_KEY` | — | Each arr needs both. Leave out the ones you don't run. |
| `SONARR_URL`, `SONARR_API_KEY` | — | |
| `LIDARR_URL`, `LIDARR_API_KEY` | — | |
| `RELAY_NAME` | `Q8 Relay` | How it's listed in Q8. |
| `RELAY_PORT` | `8484` | |
| `RELAY_POLL_INTERVAL` | `10s` | How often the arrs are asked. 2s at the least. |
| `RELAY_PATH_MAP` | — | `/arr/path=/relay/path`, separated by commas. |
| `RELAY_SHOW_CODE` | `true` | `false` shows the pairing code in the log only, not on the page. |
| `RELAY_ANNOUNCE` | `true` | Bonjour announcing. |

## Security

Relay is made for your home network. It speaks plain HTTP, so **don't
forward its port to the internet**. To reach it from outside, use a VPN such
as WireGuard or Tailscale.

- A pairing code works for 15 minutes, changes once it's used, and only five
  wrong codes are taken a minute.
- Anyone on your network who opens Relay's page can see the current code.
  If that matters, set `RELAY_SHOW_CODE=false` and read it from the log.
- Mount your data read-only (`:ro`). Relay never writes there. In Q8 focused
  it asks the arrs to delete, and they do it.

## Troubleshooting

**"can't see the arr's file" in the log.** Relay's mount doesn't match your
arrs'. Mount the same folder at the same path, or set `RELAY_PATH_MAP`.

**Q8 doesn't list Relay.** Bonjour can't leave a container's own network.
Type `server:8484` in Q8, or use `network_mode: host`.

**Permission denied.** Set `user:` to the same PUID:PGID your arrs run as,
so Relay can read their files and write `/config`.

**An arr won't connect.** Check its URL from inside Relay's network
(`http://radarr:7878`, not `localhost`) and its API key (Settings › General).

## What you need

An arr, a download client and Relay, all sharing one `/data` volume. A shared
volume matters for the arrs anyway: without one, hard links fail and every
import is a full copy. The [Servarr Wiki](https://wiki.servarr.com) and the
[TRaSH Guides](https://trash-guides.info) explain the folder layout.

Starting from scratch? Use each app's official image, set it up as the
Servarr Wiki describes, and add Relay as above.

### Ways to request

1. **Bare minimum.** Ask for a film on Radarr's own page.
2. **Comfortable, no extras.** Add Radarr's import lists: an IMDb or Trakt
   watchlist you add to from your phone.
3. **Most comfortable.** Add Seerr and Jellyfin or Plex. Seerr signs in
   through them. *Coming soon:* Relay marking a request Available in Seerr
   once Q8 has it.

Tip: Radarr's **Settings › Connect › On Grab** can tell your phone when a
film you asked for long ago starts downloading.

## Your responsibility

Relay works with the apps you run, on your network. You're responsible for
what you download, and for having the right to it.

## Building

```
go test -race ./...
docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/q8-app/q8-relay .
```

Dependencies: the Go standard library, plus
[grandcat/zeroconf](https://github.com/grandcat/zeroconf) for Bonjour. The
protocol Q8 speaks is in [PROTOCOL.md](PROTOCOL.md). Fork it, change it, run
your own: it's MIT, see [LICENSE](LICENSE).
