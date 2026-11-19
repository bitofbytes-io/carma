# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM golang:1.27.4-alpine3.23@sha256:f70850ef6ce341e8b6427f677049145d63c0b7ce002d4d7e3d464828b1b96dcd AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/carma ./cmd/carma \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/carma-migrate ./cmd/carma-migrate \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/carma-reminders ./cmd/carma-reminders

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
WORKDIR /app
RUN apk add --no-cache ca-certificates wget \
 && addgroup -g 10001 -S carma \
 && adduser -u 10001 -S -G carma carma \
 && mkdir -p /data/assets \
 && chown -R carma:carma /app /data/assets
COPY --from=builder /out/carma /out/carma-migrate /out/carma-reminders ./
COPY --chown=carma:carma static ./static
ENV PORT=4700 ASSET_ROOT=/data/assets
USER carma
EXPOSE 4700
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -q -O - http://127.0.0.1:4700/health || exit 1
CMD ["./carma"]
