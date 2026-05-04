# AGENTS.md

## Repo Shape

- This repo is a minimal Nix + Kind + Tilt local Kubernetes setup, not a general Go monorepo yet.
- The only app is `services/hello-world`, a Go 1.22 HTTP service built by `services/hello-world/Dockerfile` and deployed by `k8s/hello-world.yaml`.
- `Tiltfile` is the wiring source of truth for dev orchestration; `flake.nix` is the source of truth for available CLI dependencies.

## Commands

- Use `nix run .#setup-kind` before Tilt if the cluster/context may not exist; it creates/selects the `kind-openstaticfish` context.
- Use `nix run .#tilt -- up` instead of calling a bare `tilt` from outside `nix develop`; the wrapper adds `kind`, `kubectl`, and `docker` to `PATH` so Tilt can load images into Kind.
- Use `nix run .#tilt -- down` to remove Tilt-managed resources.
- Use `nix develop` when running repeated manual commands; inside it, `setup-kind`, `tilt`, `kubectl`, `docker`, and `go` are available.
- Use `nix flake show` as the quick flake sanity check after editing `flake.nix`.

## Gotchas

- Nix flakes only see git-tracked or staged files. After adding files referenced by the flake, Tiltfile, Dockerfile, or manifests, run `git add <file>` or `git add -A` before `nix run` verification.
- `Tiltfile` intentionally allows only `kind-openstaticfish` via `allow_k8s_contexts`; do not switch it to a broad context unless explicitly requested.
- Do not re-add `default_registry('localhost:5000')` unless a local registry is also created; this setup relies on Tilt loading images directly into Kind.
- The hello-world Dockerfile intentionally copies only `go.mod`; there is no `go.sum` because the app currently has no external Go dependencies.
- Docker must be running and reachable by the user before `setup-kind` or Tilt image builds can work.

## Verification

- For infra config changes, run `nix flake show`.
- For cluster/Tilt changes, the focused path is `nix run .#setup-kind` then `nix run .#tilt -- up`, followed by `curl http://localhost:8080`.
- There is no test, lint, formatter, CI, or pre-commit config in this repo yet; do not invent commands.
