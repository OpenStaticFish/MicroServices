# OpenStaticFish MicroServices

Basic local Kubernetes development setup using Nix, Kind, Tilt, Docker, and a small Go hello-world service.

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

Test the hello-world service:

```bash
curl http://localhost:8080
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
kubectl logs -f deployment/hello-world-deployment
```

Stream logs from the hello-world deployment.

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
docker build -t hello-world ./services/hello-world
```

Build the hello-world image manually outside Tilt.

## Layout

```text
.
├── flake.nix
├── Tiltfile
├── k8s/
│   └── hello-world.yaml
├── scripts/
│   └── setup-kind
└── services/
    └── hello-world/
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
