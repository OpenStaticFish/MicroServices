# OpenStaticFish MicroServices

Basic local Kubernetes development setup using Nix, Kind, Tilt, Docker, and small Go microservices.

## Requirements

- Nix with flakes enabled
- Docker running and reachable by your user
- Git

All project CLI dependencies are provided by `flake.nix`, including:

- `tilt`
- `kind`
- `kubectl`
- `docker`
- `helm`
- `go`
- `doppler`

## Quick Start

Nuke any stale state, create a fresh Kind cluster, and start Tilt:

```bash
nix run .#tilt-up
```

Open the Tilt UI:

```text
http://localhost:10350
```

Test the scraper service:

```bash
curl http://localhost:8080/health
```

Analyze a site once Tilt is running:

```bash
scripts/analyze-site adaptive.co.uk
```

Check Lightpanda once Tilt is running:

```bash
curl http://localhost:9222/json/version
curl http://localhost:8000/healthz
```

Nuke Tilt + Kind cluster:

```bash
nix run .#tilt-down
```

## Dev Shell

You can also enter the Nix development shell and run commands directly:

```bash
nix develop
```

Inside the shell:

```bash
tilt-up            # nuke stale state, recreate Kind cluster, start Tilt
tilt-down          # nuke Tilt + Kind cluster
tilt up            # start Tilt (assumes cluster exists)
tilt down           # tear down Tilt resources only
kubectl get pods
docker ps
doppler secrets     # list Doppler project secrets
```

## Project Commands

### Nix Commands

```bash
nix develop
```

Enter the dev shell with all dependencies on `PATH`.

```bash
nix run .#setup-kind
```

Create the `openstaticfish` Kind cluster if needed, select the `kind-openstaticfish` Kubernetes context, and print cluster info.

```bash
nix run .#tilt -- up
```

Start Tilt with the required runtime tools, including `kind`, `kubectl`, and `docker`. Assumes the Kind cluster already exists and the context is set.

```bash
nix run .#tilt-down
```

Nuke Tilt + Kind cluster: kills any stale Tilt process on port 10350, then deletes the Kind cluster.

```bash
nix run .#tilt-up
```

Nuke stale state, create a fresh Kind cluster, set the kube context, then start Tilt. Use this after switching worktrees or crashes.

```bash
nix run .#tilt -- down
```

Tear down only the Tilt-managed Kubernetes resources without deleting the cluster.

```bash
nix flake show
```

Validate and display flake outputs.

### Tilt Commands

```bash
tilt up
```

Build images, deploy Kubernetes manifests, start file watching, and open the Tilt UI.

```bash
tilt down
```

Remove deployed Tilt resources from the current Kubernetes context.

```bash
tilt logs
```

Stream aggregated logs from Tilt resources.

```bash
tilt status
```

Show current resource status.

```bash
tilt ui
```

Open the Tilt web UI.

### Kubernetes Commands

```bash
kubectl config use-context kind-openstaticfish
```

Select the project Kind cluster.

```bash
kubectl cluster-info
```

Verify Kubernetes connectivity.

```bash
kubectl get pods
```

List running pods.

```bash
kubectl logs -f deployment/site-analyzer-deployment
```

Stream logs from the site analyzer deployment.

```bash
kubectl get svc
```

List Kubernetes services.

### Docker Commands

```bash
docker ps
```

List running containers, including Kind node containers.

```bash
docker build -t site-analyzer ./services/site-analyzer
```

Build the site analyzer image manually outside Tilt.

## Site Analyzer

The `site-analyzer` service accepts a URL and returns JSON describing visible site infrastructure and technology signals.

Endpoint:

```text
POST http://localhost:8090/analyze
```

Request:

```json
{
  "url": "https://example.com"
}
```

Wrapper script:

```bash
scripts/analyze-site https://example.com
```

Override the endpoint if needed:

```bash
scripts/analyze-site --endpoint http://localhost:8090/analyze example.com
```

Load-test a running analyzer with fixed request and concurrency levels:

```bash
scripts/load-test-site-analyzer --url https://example.com --requests 100 --concurrency 10
```

Single-instance benchmark from the local Kind deployment, targeting `https://example.com` with `1000` requests per run and `MAX_CONCURRENT_ANALYSES=20`:

| Concurrency | HTTP 200 | HTTP 429 | Throughput | Avg latency | Max latency |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 5 | 1000 | 0 | 136.23 req/s | 0.029s | 0.068s |
| 10 | 1000 | 0 | 248.62 req/s | 0.032s | 0.047s |
| 15 | 1000 | 0 | 346.15 req/s | 0.034s | 0.052s |
| 20 | 1000 | 0 | 391.14 req/s | 0.039s | 0.061s |
| 25 | 759 | 241 | 535.11 req/s | 0.035s | 0.074s |
| 30 | 688 | 312 | 590.35 req/s | 0.036s | 0.292s |

Current guidance: treat `20` concurrent in-flight analyses as the safe per-instance ceiling. Above that, the service intentionally sheds load with `429` instead of queueing unbounded work.

Operational endpoints:

- `GET /health`: cheap liveness check
- `GET /ready`: readiness check; returns unavailable when the pod is saturated
- `GET /metrics`: Prometheus-style counters and gauges

Runtime controls:

- `MAX_CONCURRENT_ANALYSES`: maximum in-flight analyses per pod before returning `429`
- `ANALYSIS_TIMEOUT`: whole-analysis deadline, default `15s`
- `FETCH_TIMEOUT`: outbound HTTP fetch deadline, default `10s`
- `MAX_REQUEST_BYTES`: request body cap, default `4096`
- `MAX_RESPONSE_BYTES`: fetched response body cap, default `2097152`

The response includes:

- `hosting.provider`: the visible edge/provider inferred from DNS, reverse DNS, and HTTP headers
- `hosting.cdn`: CDN detected from headers, for example Cloudflare, Fastly, CloudFront, or Akamai
- `hosting.origin_provider`: best-effort origin/platform inference, for example Pantheon when `pantheonsite.io` hints are exposed
- `hosting.origin_evidence`: specific signals used for origin inference
- `dns`: nameservers and common DNS records
- `technologies`: CMS/framework/server signals such as Drupal, WordPress, React, Next.js, or Cloudflare
- `security`: TLS/HSTS/security-header information

CDNs can hide the real origin. If a site is proxied through Cloudflare, the public DNS and IPs usually identify Cloudflare rather than the origin host. Origin detection is therefore best-effort and depends on leaked signals such as CSP entries, headers, HTML references, or provider-specific domains.

## Lightpanda Browser

The Lightpanda services provide a lightweight browser runtime for parallel automation workflows and agents.

### CDP Service

The `lightpanda-cdp` service runs the official `lightpanda/browser:nightly` image and exposes Lightpanda's Chrome DevTools Protocol server.

Local endpoint:

```text
http://localhost:9222
```

Health/version check:

```bash
curl http://localhost:9222/json/version
```

Clients can connect with Puppeteer or another CDP-compatible client. In local Tilt, use `localhost:9222`. In production, route through:

```text
http://apps.silverside-gopher.ts.net/lightpanda-cdp
```

If a client needs the WebSocket URL directly in production, keep the `/lightpanda-cdp` prefix on the WebSocket path so Traefik can route it to the service.

### MCP HTTP Service

The `lightpanda-mcp` service wraps `lightpanda mcp` with `supergateway` and exposes MCP Streamable HTTP for remote agents.

Local endpoint:

```text
http://localhost:8000/mcp
```

Health check:

```bash
curl http://localhost:8000/healthz
```

Example MCP initialize request:

```bash
curl -X POST http://localhost:8000/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl-test","version":"1.0"}}}'
```

Production endpoint:

```text
http://apps.silverside-gopher.ts.net/lightpanda-mcp/mcp
```

### Concurrent Search via MCP

`scripts/test-lightpanda-search` demonstrates concurrent browser automation via the MCP endpoint. It searches Bing (Google and DuckDuckGo serve bot challenges in automated environments) and extracts organic result titles and URLs:

```bash
# Single search, 5 results
scripts/test-lightpanda-search

# 5 concurrent searches
scripts/test-lightpanda-search --concurrent 5

# Custom query
scripts/test-lightpanda-search --query "kubernetes helm" --results 3 --concurrent 5
```

Each worker initializes a stateful MCP session, navigates to Bing, evaluates JavaScript to extract `li.b_algo h2 a` elements, and decodes Bing redirect URLs to their real destinations.

### CDP Screenshot

`scripts/screenshot-lightpanda-page` captures a PNG via the CDP WebSocket endpoint. Lightpanda has no graphical rendering engine, so CDP screenshots return a blank canvas. For real visual screenshots, run a graphical headless Chrome service such as `browserless/chrome`:

```bash
# Bing search (Lightpanda CDP — no visual output)
scripts/screenshot-lightpanda-page

# Custom query
scripts/screenshot-lightpanda-page --query "kubernetes helm"

# Custom URL
scripts/screenshot-lightpanda-page --url https://example.com --output /tmp/example.png
```

### Scaling

Local Tilt starts one pod of each Lightpanda service. Scale manually in Kind when testing parallel workflows:

```bash
kubectl scale deployment/lightpanda-cdp-deployment --replicas=3
kubectl scale deployment/lightpanda-mcp-deployment --replicas=3
```

Production manifests start two replicas of each Lightpanda service and include HPAs that can scale each deployment up to ten pods based on CPU utilization.

## Secrets

Runtime secrets are managed with Doppler in the `openstaticfish-microservices` project using the `dev` config.

Initial setup after cloning:

```bash
doppler login
doppler setup --no-interactive
```

Run commands with secrets injected:

```bash
doppler run -- your-command
```

Tilt installs the Doppler Kubernetes Operator and applies `k8s/doppler-webshare-secret.yaml` to sync Doppler project `openstaticfish-microservices` config `dev` into the local Kubernetes `webshare-api` secret for the scraper service.

The operator needs a Kubernetes token secret in the `doppler-operator-system` namespace. Create it once after the Kind cluster exists:

```bash
kubectl create namespace doppler-operator-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic doppler-token-secret \
  --namespace doppler-operator-system \
  --from-literal=serviceToken="$(doppler configure get token --plain)" \
  --dry-run=client \
  -o yaml | kubectl apply -f -
```

Do not commit local `.env` files or token values; `.env` and `.env.*` are ignored by Git.

## Production Deployment

Tilt is local-development only. HetznerTerra owns the K3s cluster and platform plumbing; this repo owns application source code, production images, production Kubernetes manifests, app routes, and app ExternalSecrets.

Production Kubernetes manifests live in `deploy/prod`. Flux applies that path from a Gitea pull mirror of this GitHub repo:

```text
ssh://git@64.176.189.59:2222/OpenStaticFish/MicroServices.git
```

The public Tailnet hostname is:

```text
apps.silverside-gopher.ts.net
```

On pushes to `main`, GitHub Actions builds and pushes these GHCR images:

- `ghcr.io/openstaticfish/microservices/site-analyzer:<git-sha>`
- `ghcr.io/openstaticfish/microservices/scraper:<git-sha>`
- `ghcr.io/openstaticfish/microservices/lightpanda-mcp:<git-sha>`

The workflow also pushes `:main` as a convenience tag, but production uses the immutable `<git-sha>` tags in `deploy/prod/kustomization.yaml`. The prebuilt `lightpanda-cdp` image is pinned by digest in its Deployment. After app images are pushed, CI updates `deploy/prod/kustomization.yaml` to the new SHA and commits the tag update back to `main` with `[skip ci]`. The Gitea mirror pulls the update, then Flux reconciles `./deploy/prod` into the `microservices` namespace.

Runtime secrets stay in Doppler project `openstaticfish-microservices`, config `dev`. Production manifests represent them only as External Secrets using `ClusterSecretStore/doppler-openstaticfish-microservices`. Do not put runtime secrets in GitHub, Gitea, images, or manifests.

### Adding a Service

1. Add the service source under `services/<service-name>/` with a `Dockerfile`.
2. Add one Kubernetes object per file under `deploy/prod/`, using kebab-case filenames:
   `deploy/prod/<service-name>-deployment.yaml`, `deploy/prod/<service-name>-service.yaml`, and optionally `deploy/prod/<service-name>-hpa.yaml`.
3. Configure the Deployment with health/readiness probes, resource requests/limits, and an image in `ghcr.io/openstaticfish/microservices/<service-name>:<sha>`.
4. Add those files to `deploy/prod/kustomization.yaml`.
5. Add an `images:` entry in `deploy/prod/kustomization.yaml` for `ghcr.io/openstaticfish/microservices/<service-name>`.
6. Add the service to `.github/workflows/publish-images.yml` under `matrix.service` and update the tag-update step for the new image.
7. Add a route in `deploy/prod/ingressroute-microservices.yaml` for ``Host(`apps.silverside-gopher.ts.net`) && PathPrefix(`/service-name`)``.
8. Add the prefix to `deploy/prod/traefik-middleware-strip-prefix.yaml` if the app should receive paths without the route prefix.
9. Update local Tilt files only if the service should also run in local Kind development.

### Adding Runtime Secrets

1. Add the secret value in Doppler project `openstaticfish-microservices`, config `dev`.
2. Add or update an ExternalSecret in `deploy/prod/` using this store:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: example-secret
  namespace: microservices
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: doppler-openstaticfish-microservices
    kind: ClusterSecretStore
  target:
    name: example-secret
    creationPolicy: Owner
  data:
    - secretKey: app-key
      remoteRef:
        key: DOPPLER_SECRET_NAME
```

3. Reference the generated Kubernetes Secret from the Deployment with `valueFrom.secretKeyRef`.
4. Add the ExternalSecret file to `deploy/prod/kustomization.yaml`.

Never commit secret values, Doppler service tokens, GHCR tokens, or generated Kubernetes Secret manifests.

### Production Verification

Validate manifests locally before pushing:

```bash
kubectl kustomize deploy/prod
git diff --check
```

If `actionlint` is installed, validate the workflow:

```bash
actionlint .github/workflows/publish-images.yml
```

Useful cluster checks after Flux reconciles:

```bash
kubectl -n microservices get deploy,svc,hpa,pods
kubectl -n microservices get ingressroute,middleware
kubectl -n microservices get externalsecret,secret
kubectl -n microservices describe externalsecret webshare-api
kubectl -n microservices rollout status deployment/scraper
kubectl -n microservices rollout status deployment/site-analyzer
kubectl -n microservices rollout status deployment/lightpanda-mcp
kubectl -n microservices rollout status deployment/lightpanda-cdp
```

Useful Flux checks from the cluster context:

```bash
flux get sources git -A
flux get kustomizations -A
flux reconcile source git <source-name> -n <flux-namespace>
flux reconcile kustomization <kustomization-name> -n <flux-namespace>
```

Application health checks through the Tailnet route:

```bash
curl http://apps.silverside-gopher.ts.net/scraper/health
curl http://apps.silverside-gopher.ts.net/site-analyzer/health
curl http://apps.silverside-gopher.ts.net/lightpanda-cdp/json/version
curl http://apps.silverside-gopher.ts.net/lightpanda-mcp/healthz
```

## Layout

```text
.
├── flake.nix
├── doppler.yaml
├── Tiltfile
├── deploy/
│   └── prod/
│       ├── kustomization.yaml
│       ├── ingressroute-microservices.yaml
│       ├── namespace.yaml
│       ├── lightpanda-cdp-deployment.yaml
│       ├── lightpanda-cdp-hpa.yaml
│       ├── lightpanda-cdp-service.yaml
│       ├── lightpanda-mcp-deployment.yaml
│       ├── lightpanda-mcp-hpa.yaml
│       ├── lightpanda-mcp-service.yaml
│       ├── scraper-deployment.yaml
│       ├── scraper-hpa.yaml
│       ├── scraper-service.yaml
│       ├── site-analyzer-deployment.yaml
│       ├── site-analyzer-hpa.yaml
│       ├── site-analyzer-service.yaml
│       ├── traefik-middleware-strip-prefix.yaml
│       └── webshare-api-externalsecret.yaml
├── k8s/
│   ├── lightpanda-cdp.yaml
│   ├── lightpanda-mcp.yaml
│   ├── doppler-webshare-secret.yaml
│   ├── scraper.yaml
│   └── site-analyzer.yaml
├── scripts/
│   ├── analyze-site
│   ├── load-test-site-analyzer
│   ├── screenshot-lightpanda-page
│   ├── setup-kind
│   └── test-lightpanda-search
└── services/
    ├── scraper/
    │   ├── Dockerfile
    │   ├── go.mod
    │   └── main.go
    ├── lightpanda-mcp/
    │   └── Dockerfile
    └── site-analyzer/
        ├── Dockerfile
        ├── go.mod
        └── main.go
```

## Notes

Nix flakes only see files that are tracked by Git. If `nix run` says a path is not visible to Nix, stage the file:

```bash
git add <file>
```

For this repo setup, staging all project files is usually enough:

```bash
git add -A
```

The Tiltfile allows only the `kind-openstaticfish` Kubernetes context to avoid accidentally deploying to another cluster.

If Tilt reports that it cannot connect to Kubernetes, run:

```bash
nix run .#tilt-up
```

This nukes stale state, recreates the Kind cluster, and starts Tilt.
