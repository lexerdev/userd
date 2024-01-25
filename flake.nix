{
  description = ''
    Userd gathers user account information from the specified git repository,
    then administrates the Linux accounts and their ssh access across particular servers.
  '';

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-23.11";
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, flake-utils, nixpkgs }:
    flake-utils.lib.simpleFlake {
      inherit self nixpkgs;
      name = "userd";
      shell = ./shell.nix;
    };
}

