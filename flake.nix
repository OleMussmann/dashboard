{
  description = "Dashboard API - homelab monitoring backend";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};
    in
    {
      packages = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          dashboard-api = pkgs.callPackage ./nix/package.nix { };
          dashboard-api-image = pkgs.callPackage ./nix/api-image.nix {
            dashboard-api = self.packages.${system}.dashboard-api;
          };
          default = self.packages.${system}.dashboard-api;
        }
      );

      nixosModules = {
        agent = import ./nix/agent-module.nix;
      };

      devShells = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              gopls
              gotools
              go-tools # staticcheck
            ];

            shellHook = ''
              echo "dashboard-api dev shell"
              echo "  go $(go version | cut -d' ' -f3)"
            '';
          };
        }
      );
    };
}
