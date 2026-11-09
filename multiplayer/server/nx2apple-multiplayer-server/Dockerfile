FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nx2apple-mp ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/coop-relay ./cmd/coop-relay

FROM alpine:3.21
RUN adduser -D -H -u 10001 app
COPY --from=build /out/nx2apple-mp /out/coop-relay /usr/local/bin/
USER app
# nx2apple-mp: 8080/tcp (behind Caddy), 3479/udp. coop-relay (--entrypoint /usr/local/bin/coop-relay): 47610/tcp
EXPOSE 8080/tcp 3479/udp 47610/tcp
ENTRYPOINT ["/usr/local/bin/nx2apple-mp"]
