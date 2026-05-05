# --- Lightpanda MCP Service Dockerfile ---
# Exposes Lightpanda's stdio MCP server over Streamable HTTP for remote agents.

FROM lightpanda/browser:nightly AS lightpanda

FROM oven/bun:1.3.0-debian

RUN apt-get update -yq && \
    apt-get install -yq --no-install-recommends ca-certificates tini && \
    rm -rf /var/lib/apt/lists/*

RUN bun add --global supergateway@3.4.3

COPY --from=lightpanda /bin/lightpanda /usr/local/bin/lightpanda

ENV LIGHTPANDA_DISABLE_TELEMETRY=true

EXPOSE 8000

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["supergateway", "--stdio", "lightpanda mcp", "--outputTransport", "streamableHttp", "--stateful", "--sessionTimeout", "180000", "--streamableHttpPath", "/mcp", "--healthEndpoint", "/healthz", "--port", "8000", "--logLevel", "info"]
