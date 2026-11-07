# NX2Apple Multiplayer Directory (prototype)

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
