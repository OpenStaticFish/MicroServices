# --- Site Analyzer Service Dockerfile ---
# Multi-stage build for minimal image size

FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /site-analyzer .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /site-analyzer /site-analyzer
EXPOSE 8090
ENTRYPOINT ["/site-analyzer"]
