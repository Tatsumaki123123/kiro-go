FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 编译主程序（Linux）
RUN CGO_ENABLED=0 GOOS=linux go build -o kiro-api-proxy .
# 交叉编译 AdsPower CORS 中继工具（Windows amd64）
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o adspower-relay.exe ./adspower-relay/

FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/kiro-api-proxy .
COPY --from=builder /app/adspower-relay.exe ./downloads/adspower-relay.exe
COPY --from=builder /app/web ./web

EXPOSE 8080
VOLUME /app/data

CMD ["./kiro-api-proxy"]
