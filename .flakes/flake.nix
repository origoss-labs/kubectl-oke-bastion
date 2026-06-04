{
  description = "Dev/CI toolchain for kubectl-oke-bastion — every tool comes from here (ADR-0008)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          # go.mod requires go 1.26; pin the matching toolchain explicitly.
          packages = [
            pkgs.go_1_26
            pkgs.golangci-lint
            pkgs.gofumpt
            pkgs.oci-cli
            pkgs.goreleaser
            pkgs.pre-commit
            pkgs.yamllint
            pkgs.krew
          ];
        };
      }
    );
}
