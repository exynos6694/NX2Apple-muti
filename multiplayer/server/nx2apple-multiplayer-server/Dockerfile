FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nx2apple-mp ./cmd/server

FROM alpine:3.21
RUN adduser -D -H -u 10001 app
COPY --from=build /out/nx2apple-mp /usr/local/bin/nx2apple-mp
USER app
EXPOSE 8080/tcp 3479/udp
ENTRYPOINT ["/usr/local/bin/nx2apple-mp"]
