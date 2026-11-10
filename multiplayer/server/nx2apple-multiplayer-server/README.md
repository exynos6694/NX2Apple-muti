# NX2Apple Multiplayer Directory (prototype) and coop-relay

This module holds two programs:

- `cmd/coop-relay`: the relay for internet play. It works today with patch 0124 (`DM_COOP`). See "Internet play through coop-relay" below.
- `cmd/server`: a directory and signaling prototype for later (automatic matching, UDP hole punching). The engine does not talk to it yet.

## Internet play through coop-relay

Local wireless play (`DM_COOP=auto`) finds hosts on the same Wi-Fi. With a relay, players on different networks meet the same way:
everybody connects out to the relay, nobody forwards a port, and it works behind any NAT, CGNAT and mobile data included.

- The host opens its room at the relay while the game hosts. A visitor's scan asks the relay for the room and the game lists the host's
  island as on a LAN. The relay pairs the visitor's link with one the host dials for it and copies bytes between them.
- The room code decides who meets. The relay only learns a 16-byte room id derived one-way from it (HKDF in `hle_coop_auth.c`). Every
  link proves the code both ways and every frame carries a tag, so the relay cannot forge or change game messages. It can read them:
  nothing is encrypted.
- Through a relay the game's datagrams travel over the TCP link. Latency is the round trip to the relay and back.

### Players

Nothing to set up. The launcher's "Local wireless play" switch (`DM_COOP=auto`) plays through the built-in relay (patch 0125,
`COOP_RELAY_BUILTIN` in `coop.h`: 158.180.89.88:47610) in one room per game, derived from the title ID (`coop_room_for_title`). Then
play as on a LAN: the host invites, the visitor looks for a local connection. The same main build and the same cheats are required on
every side, and one player at a time can host each game.

Overrides, in `env.txt` (Mac: `~/Library/Application Support/nxnative/<title id>/env.txt`, iPhone: `Documents/env.txt`):

- `DM_COOP_ROOM=XXXX-XXXX-XXXX-XXXX`: a private room, the same code on every side.
- `DM_COOP_RELAY=<IPv4>[:<port>]`: another relay.
- `DM_COOP_INTERNET=0`: this Wi-Fi only (DNS-SD, no server), as before patch 0125.

### The server

`coop-relay` listens on 127.0.0.1 unless told otherwise. On a server:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o coop-relay ./cmd/coop-relay   # amd64 for x86 servers
scp coop-relay deploy/coop-relay.service <server>:
ssh <server> 'sudo install -m 755 coop-relay /usr/local/bin/ && sudo install -m 644 coop-relay.service /etc/systemd/system/ \
  && sudo systemctl daemon-reload && sudo systemctl enable --now coop-relay'
```

Or with Docker: `docker run -d --restart unless-stopped -p 47610:47610 --entrypoint /usr/local/bin/coop-relay <image> -listen 0.0.0.0:47610`.

Open 47610/tcp in the cloud firewall and on the host. On Oracle Cloud that is an ingress rule in the VCN's security list (or the
instance's NSG), and on Oracle's Ubuntu and Oracle Linux images also the host's own rules, for example
`sudo iptables -I INPUT -p tcp --dport 47610 -j ACCEPT` (save it with `netfilter-persistent save` on Ubuntu) or
`sudo firewall-cmd --permanent --add-port=47610/tcp && sudo firewall-cmd --reload`.

Limits (flags): 64 rooms, 7 visitors a room, 256 links, 24 links per address, 10 new links a second per address, 60 lookups of absent
rooms a minute per address, a room lives 12 hours and carries 4 GiB, 256 KiB a second per direction of a pair. The log names rooms by
the first 4 bytes of their id and never records payloads. `journalctl -u coop-relay` shows it.

## The directory prototype

This is the first server-side implementation slice for the agreed model:
- one public presence space; no rooms or room codes;
- automatic grouping by `title_id + game_version + protocol`;
- HTTP signaling and a UDP rendezvous endpoint;
- presence expires after 45 seconds without heartbeat;
- game traffic is **not** relayed through this service.

## Important current boundary

This service is a directory/signaling prototype, not a complete LDN implementation.
The emulator-side `nn::ldn` state machine and BSD socket hooks are not implemented by
this package. It does not make a game multiplayer by itself. The UDP registration
packet reports the address observed by the rendezvous server; peers still need to
exchange endpoints and perform simultaneous UDP hole punching. Symmetric NAT/CGNAT
may prevent direct P2P. No relay is included.

The first version deliberately uses HTTP polling for signaling so it has no external
Go dependencies. Put HTTPS/WSS-equivalent HTTP transport behind the existing Caddy
instance; do not expose the HTTP listener directly to the public internet.

## Run locally

```sh
go test ./...
HTTP_ADDR=:8080 UDP_ADDR=:3479 go run ./cmd/server
```

## API

Register presence:

```http
POST /v1/presence
Content-Type: application/json

{
  "client_id": "random-stable-client-id",
  "nickname": "player",
  "game": {
    "title_id": "0100000000000000",
    "game_version": "1.0.0",
    "protocol": "ldn-v1"
  }
}
```

Heartbeat every 15 seconds:

```http
POST /v1/heartbeat
{"client_id":"random-stable-client-id"}
```

List compatible players:

```http
GET /v1/players?title_id=0100000000000000&game_version=1.0.0&protocol=ldn-v1
```

Send signaling message (`offer`, `answer`, `candidate`, `punch`, `disconnect`):

```http
POST /v1/signals
{"from":"client-a","to":"client-b","kind":"candidate","data":{"address":"203.0.113.8:50000"}}
```

Poll signaling messages:

```http
GET /v1/signals?client_id=client-b&after=0
```

Register the observed UDP endpoint:

```http
POST /v1/udp/register
{"client_id":"random-stable-client-id"}
```

Then send one UDP datagram to `UDP_ADDR`:

```json
{"type":"register","client_id":"random-stable-client-id"}
```

The UDP response contains the source address observed by the server. Exchange that endpoint over
the signaling API and have both peers send UDP punch packets to each other.

Leave:

```http
DELETE /v1/presence/random-stable-client-id
```

## Production notes

- Allow the chosen UDP port (default `3479/udp`) in the cloud firewall and host firewall.
- Keep HTTP behind Caddy/TLS. Existing Caddy routes and containers should be preserved.
- This prototype is in-memory: restart clears presence and queued signals.
- Before public deployment, add authenticated client sessions, request rate limits,
  payload schemas for signaling messages, metrics, and abuse controls.
- Do not put game packets or user credentials in signaling payloads.
