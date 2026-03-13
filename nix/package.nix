{ lib, buildGoModule }:

buildGoModule {
  pname = "dashboard-api";
  version = "0.1.0";

  src = lib.cleanSource ./..;

  vendorHash = "sha256-QqxskmHSE1crdVD56YJrZ5GbQhsqKHgnttI7crvqteM=";

  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
  ];

  meta = with lib; {
    description = "Homelab dashboard API backend";
    license = licenses.mit;
    mainProgram = "dashboard-api";
  };
}
