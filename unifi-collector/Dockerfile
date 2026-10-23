FROM golang:1.27.3-alpine3.23 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /unifi-collector .

FROM alpine:3.23 AS runtime

COPY --from=builder /unifi-collector /unifi-collector
USER 1000:1000
ENTRYPOINT ["/unifi-collector"]