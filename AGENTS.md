# AGENTS.md

## Repo Shape

- Nix + Kind + Tilt local Kubernetes setup for Go microservices + Lightpanda browser automation.
- Three Go apps under `services/`:
  - `scraper` — web scraping service (port 8080)
  - `site-analyzer` — site analysis service (port 8090)
  - `lightpanda-mcp` — wraps Lightpanda MCP behind `supergateway` for remote agents (port 8000)
- `lightpanda-cdp` runs the official `lightpanda/browser:nightly` CDP server (port 9222); not a Go app, deployed from a prebuilt image.
- `Tiltfile` wires all services; `flake.nix` declares CLI deps and dev shell.
- No test, lint, formatter, typecheck, or pre-commit config exists.

## Commands

- `nix run .#setup-kind` — creates/selects the `kind-openstaticfish` Kind cluster.
- `nix run .#tilt -- up` — deploys all services with live reload.
- `nix run .#tilt -- down` — tears down Tilt-managed resources.
- `nix run .#tilt-up` — nukes stale Tilt + Kind state, creates/reuses Kind cluster, sets kube context, then `tilt up`. Use this after switching worktrees or crashes.
- `nix run .#tilt-down` — kills any Tilt process on port 10350 and deletes the Kind cluster.
- `nix develop` — enters the dev shell with all deps (`tilt`, `kubectl`, `docker`, `go`, `jq`, `websocat`, etc.).
- `nix flake show` — sanity check after editing `flake.nix`.

## Verification Paths

- **Infra config changes**: `nix flake show`
- **Cluster + Tilt**: `nix run .#setup-kind && nix run .#tilt -- up`
- **Scraper**: `curl http://localhost:8080/health`
- **Site analyzer**: `curl http://localhost:8090/health`
- **Lightpanda CDP**: `curl http://localhost:9222/json/version`
- **Lightpanda MCP**: `curl http://localhost:8000/healthz`

## Lightpanda Browser Automation

- `lightpanda-cdp` exposes CDP on `localhost:9222`. No graphical rendering engine — CDP screenshots return blank. Use `browserless/chrome` or similar for visual screenshots.
- `lightpanda-mcp` exposes MCP Streamable HTTP on `localhost:8000/mcp`. Stateful sessions with 3-minute timeout. Tools: `goto`, `evaluate`, `markdown`, `links`, `click`, `fill`, etc.
- `scripts/test-lightpanda-search` — concurrent Bing search via MCP. Each worker gets its own browser session. Usage:
  ```bash
  scripts/test-lightpanda-search --concurrent 5 --results 5
  ```
- `scripts/screenshot-lightpanda-page` — captures CDP screenshot (blank output — Lightpanda has no renderer). For visual screenshots use a real Chrome service.

## Gotchas

- **Nix flake visibility**: Nix only sees git-tracked or staged files. After adding files referenced by `flake.nix`, `Tiltfile`, `Dockerfile`, or manifests, run `git add <file>` or `git add -A` before `nix run` verification.
- **Tilt context**: `allow_k8s_contexts('kind-openstaticfish')` is locked to Kind; do not broaden unless explicitly requested.
- **No default registry**: Tilt loads images directly into Kind. Do not add `default_registry('localhost:5000')` unless you also create a local registry.
- **Docker must be running** before `setup-kind` or Tilt image builds.
- **Production manifests** live in `deploy/prod/`. On `main` pushes, CI builds GHCR images (`ghcr.io/openstaticfish/microservices/{site-analyzer,scraper,lightpanda-mcp}`) and commits updated `newTag` to `kustomization.yaml`.
- **Secrets**: managed by the Doppler Kubernetes Operator. `k8s/doppler-webshare-secret.yaml` syncs Doppler project `openstaticfish-microservices` / config `dev` into Kubernetes secret `webshare-api`. The cluster needs a `doppler-token-secret` in namespace `doppler-operator-system`; do not commit token values.
- **No tests, lint, or CI checks exist** in this repo yet — do not invent commands that don't exist.
