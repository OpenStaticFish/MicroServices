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

Create or select the local Kind cluster:

```bash
nix run .#setup-kind
```

Start Tilt:

```bash
nix run .#tilt -- up
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

Stop Tilt resources:

```bash
nix run .#tilt -- down
```

## Dev Shell

You can also enter the Nix development shell and run commands directly:

```bash
nix develop
```

Inside the shell:

```bash
setup-kind
tilt up
tilt down
kubectl get pods
docker ps
doppler secrets
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

Run Tilt with the required runtime tools available, including `kind`, `kubectl`, and `docker`.

```bash
nix run .#tilt -- down
```

Tear down Kubernetes resources managed by Tilt.

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

Tilt uses Doppler to create the local Kubernetes `webshare-api` secret for the scraper service. Do not commit local `.env` files; `.env` and `.env.*` are ignored by Git.

## Production Images

Tilt is local-development only in this repo. It continues to build local images named `site-analyzer` and `scraper` for the Kind/Tilt workflow.

Production deployment is owned by the HetznerTerra GitOps repo. This repo publishes container images only; it is not the source of truth for production Kubernetes deployment manifests.

On pushes to `main`, GitHub Actions builds and pushes these GHCR images:

- `ghcr.io/openstaticfish/microservices/site-analyzer:main`
- `ghcr.io/openstaticfish/microservices/site-analyzer:<git-sha>`
- `ghcr.io/openstaticfish/microservices/scraper:main`
- `ghcr.io/openstaticfish/microservices/scraper:<git-sha>`

HetznerTerra should reference immutable `<git-sha>` tags for production deployments. Runtime secrets, including the scraper Webshare configuration, must be supplied by the deployment environment and are not baked into images.

## Layout

```text
.
├── flake.nix
├── doppler.yaml
├── Tiltfile
├── k8s/
│   ├── scraper.yaml
│   └── site-analyzer.yaml
├── scripts/
│   ├── analyze-site
│   └── setup-kind
└── services/
    ├── scraper/
    │   ├── Dockerfile
    │   ├── go.mod
    │   └── main.go
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

If Tilt reports that it cannot connect to Kubernetes, rerun:

```bash
nix run .#setup-kind
```

Then restart Tilt.
