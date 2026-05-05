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
docker_build('site-analyzer', './services/site-analyzer')
docker_build('lightpanda-mcp', './services/lightpanda-mcp')

k8s_yaml('./k8s/scraper.yaml')
k8s_yaml('./k8s/site-analyzer.yaml')
k8s_yaml('./k8s/lightpanda-cdp.yaml')
k8s_yaml('./k8s/lightpanda-mcp.yaml')

local_resource(
    'doppler-operator',
    cmd='kubectl apply -f https://github.com/DopplerHQ/kubernetes-operator/releases/latest/download/recommended.yaml && kubectl wait --for=condition=Established crd/dopplersecrets.secrets.doppler.com --timeout=60s',
    labels=['utility'],
)

local_resource(
    'doppler-webshare-secret',
    cmd='kubectl apply -f ./k8s/doppler-webshare-secret.yaml',
    resource_deps=['doppler-operator'],
    labels=['utility'],
)

# Watch source files for live reload
k8s_resource(
    workload='scraper-deployment',
    port_forwards='8080:8080',
    labels=['scraper'],
)

k8s_resource(
    workload='site-analyzer-deployment',
    port_forwards='8090:8090',
    labels=['site-analyzer'],
)

k8s_resource(
    workload='lightpanda-cdp-deployment',
    port_forwards='9222:9222',
    labels=['lightpanda'],
)

k8s_resource(
    workload='lightpanda-mcp-deployment',
    port_forwards='8000:8000',
    labels=['lightpanda'],
)

# --- Local Resources ---
# Run a simple health check as a local resource
local_resource(
    'health-check',
    cmd='curl -sf http://localhost:8080/health || echo "Service not ready"',
    resource_deps=['scraper-deployment'],
    labels=['utility'],
)

local_resource(
    'site-analyzer-health-check',
    cmd='curl -sf http://localhost:8090/health || echo "Site analyzer not ready"',
    resource_deps=['site-analyzer-deployment'],
    labels=['utility'],
)

local_resource(
    'lightpanda-cdp-health-check',
    cmd='curl -sf http://localhost:9222/json/version || echo "Lightpanda CDP not ready"',
    resource_deps=['lightpanda-cdp-deployment'],
    labels=['utility'],
)

local_resource(
    'lightpanda-mcp-health-check',
    cmd='curl -sf http://localhost:8000/healthz || echo "Lightpanda MCP not ready"',
    resource_deps=['lightpanda-mcp-deployment'],
    labels=['utility'],
)
