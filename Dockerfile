FROM golang:1.25.6-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/cloudpulse ./cmd/cloudpulse

FROM alpine:3.22

RUN apk add --no-cache ca-certificates libcap && \
    addgroup -S cloudpulse && \
    adduser -S -G cloudpulse -h /app cloudpulse

WORKDIR /app

COPY --from=builder /out/cloudpulse /usr/local/bin/cloudpulse

RUN setcap cap_net_raw+ep /usr/local/bin/cloudpulse

USER cloudpulse

ENTRYPOINT ["cloudpulse"]
CMD ["run", "-config", "/app/config.json"]
