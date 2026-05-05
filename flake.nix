{
  description = "OpenStaticFish MicroServices - Tilt development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        setup-kind = pkgs.writeShellApplication {
          name = "setup-kind";
          runtimeInputs = with pkgs; [ docker kind kubectl ];
          text = ''
            exec bash ${./scripts/setup-kind} "$@"
          '';
        };
        tilt-dev = pkgs.writeShellApplication {
          name = "tilt-dev";
          runtimeInputs = with pkgs; [ docker kind kubectl tilt ];
          text = ''
            exec tilt "$@"
          '';
        };
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # --- Core Dependencies ---
            tilt
            kubectl
            kubernetes-helm
            kind
            docker
            docker-compose

            # --- Secrets Management ---
            doppler

            # --- Language Toolchain ---
            go

            # --- Utilities ---
            curl
            jq
            websocat
            yq-go
          ];

          shellHook = ''
            if [ "''${OPENSTATICFISH_QUIET_SHELL:-}" != "1" ]; then
              echo "🐟 OpenStaticFish MicroServices Dev Shell"
              echo "-----------------------------------------"

            # --- Tilt Commands ---
            # Create/select the local Kind cluster first:
            #   setup-kind
            #   nix run .#setup-kind
            #
            # Start the development environment:
            #   tilt up
            #   nix run .#tilt -- up
            #
            # Tear down resources:
            #   tilt down
            #
            # View Tilt UI:
            #   tilt ui
            #
            # Check Tilt version:
            #   tilt version

            # --- Kubernetes Commands ---
            # Check cluster connection:
            #   kubectl cluster-info
            #
            # Select the project Kind cluster:
            #   kubectl config use-context kind-openstaticfish
            #
            # List running pods:
            #   kubectl get pods
            #
            # View service logs:
            #   kubectl logs -f deployment/scraper

            # --- Docker Commands ---
            # Build images manually:
            #   docker build -t scraper ./services/scraper
            #
            # List running containers:
            #   docker ps

              echo ""
              echo "Available commands:"
              echo "  setup-kind     - Create/select local Kind cluster"
              echo "  tilt up        - Start dev environment"
              echo "  tilt down      - Stop dev environment"
              echo "  kubectl ...    - Interact with k8s cluster"
              echo "  docker ...     - Interact with containers"
              echo "  doppler ...    - Manage project secrets"
              echo ""
            fi
          '';
        };

        # --- Command wrappers ---
        # Run with: nix run .#setup-kind
        # Run with: nix run .#tilt -- up
        apps = {
          setup-kind = {
            type = "app";
            program = "${setup-kind}/bin/setup-kind";
          };

          tilt = {
            type = "app";
            program = "${tilt-dev}/bin/tilt-dev";
          };
        };
      }
    );
}
