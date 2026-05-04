# --- Hello World Service Dockerfile ---
# Multi-stage build for minimal image size

FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /hello-world .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /hello-world /hello-world
EXPOSE 8080
ENTRYPOINT ["/hello-world"]
