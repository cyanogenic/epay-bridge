FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/epay-bridge ./cmd/epay-bridge

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S epay \
    && adduser -S -G epay -H -s /sbin/nologin epay
COPY --from=build /out/epay-bridge /usr/local/bin/epay-bridge
USER epay:epay
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/epay-bridge"]

