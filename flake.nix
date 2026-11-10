{
  description = "kinrec: macOS screen + system audio + microphone recorder CLI";

  inputs = {
    # The last nixpkgs release that can still build for Intel Macs.
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    # The systems to build for; a consuming flake can swap the list.
    systems.url = "github:nix-systems/default";
  };

  outputs =
    { nixpkgs, systems, ... }:
    let
      inherit (nixpkgs) lib;
      forSystems = list: f: lib.genAttrs list (system: f nixpkgs.legacyPackages.${system});
      forAllSystems = forSystems (import systems);
      # kinrec is built on ScreenCaptureKit: the binary exists only for macOS.
      forDarwinSystems = forSystems (lib.filter (lib.hasSuffix "-darwin") (import systems));
    in
    {
      # `nix build`, or `nix run . -- record -o x.mp4 --audio`.
      packages = forDarwinSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "kinrec";
          version = lib.head (
            builtins.match ''.*const Version = "([^"]+)".*'' (builtins.readFile ./kinrec.go)
          );
          src = ./.;
          # After a go.mod / go.sum change: set to lib.fakeHash, build, and
          # copy the hash from the error.
          vendorHash = "sha256-NHqM86PCwqdFATtjhh38RZVrERj3iiF7HqBWcq4kOZw=";
          subPackages = [ "cmd/kinrec" ];

          # Compile the ObjC dylib over the committed prebuilt one, for this
          # architecture only. -B because nix resets mtimes, so make would
          # call the committed one up to date.
          preBuild = "make -B dylib ARCHES=${pkgs.stdenv.hostPlatform.darwinArch}";
          # Load it from the store instead of extracting the embedded copy to
          # ~/Library/Caches/kinrec at runtime.
          postInstall = "install -Dm755 internal/dylib/libkinrec_writer.dylib -t $out/lib";
          ldflags = [
            "-X github.com/LocalKinAI/kinrec.DylibPath=${placeholder "out"}/lib/libkinrec_writer.dylib"
          ];

          meta = {
            description = "macOS screen + system audio + microphone recorder CLI";
            homepage = "https://github.com/LocalKinAI/kinrec";
            license = lib.licenses.mit;
            mainProgram = "kinrec";
          };
        };
      });

      # `nix develop`: Go plus the linters `make lint` looks for. No C compiler,
      # so `make dylib` keeps using Apple's clang for the universal dylib.
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShellNoCC {
          packages = with pkgs; [
            go
            gopls
            go-tools # staticcheck
            golangci-lint
            gnumake
          ];
        };
      });

      # `nix fmt`
      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };
}
