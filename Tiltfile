# --- OpenStaticFish MicroServices Tiltfile ---
#
# Nix-managed dependencies:
#   Run `nix develop` or use `nix run .#tilt -- up` to enter the dev shell
#   with tilt, kubectl, docker, and all other deps available.
#
# Quick reference:
#   nix run .#setup-kind  - Create/select local Kind cluster
#   tilt up        - Start all services (live reload enabled)
#   tilt down      - Tear down all deployed resources
#   tilt ui        - Open the Tilt web UI (default: http://localhost:10350)
#   tilt logs      - Stream aggregated logs
#   tilt status    - Show resource status

# --- Global Settings ---
# Nix command wrapper:
#   nix run .#setup-kind
#
# This creates/selects the supported local Kind context below.
allow_k8s_contexts('kind-openstaticfish')

# --- Scraper Service ---
# Builds the Docker image and deploys the manifest with live reload
docker_build('scraper', './services/scraper')

k8s_yaml('./k8s/scraper.yaml')

local_resource(
    'webshare-secret',
    cmd='set -a; [ ! -f .env ] || . ./.env; set +a; if [ -n "$WEBSHARE_API_KEY" ] && [ -n "$WEBSHARE_PROXY_USERNAME" ] && [ -n "$WEBSHARE_PROXY_PASSWORD" ]; then kubectl create secret generic webshare-api --from-literal=api-key="$WEBSHARE_API_KEY" --from-literal=proxy-username="$WEBSHARE_PROXY_USERNAME" --from-literal=proxy-password="$WEBSHARE_PROXY_PASSWORD" --dry-run=client -o yaml | kubectl apply -f -; else echo "Webshare runtime config missing; country scraping unavailable"; fi',
    labels=['utility'],
)

# Watch source files for live reload
k8s_resource(
    workload='scraper-deployment',
    port_forwards='8080:8080',
    resource_deps=['webshare-secret'],
    labels=['scraper'],
)

# --- Local Resources ---
# Run a simple health check as a local resource
local_resource(
    'health-check',
    cmd='curl -sf http://localhost:8080/health || echo "Service not ready"',
    resource_deps=['scraper-deployment'],
    labels=['utility'],
)
