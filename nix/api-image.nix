{ lib, dockerTools, dashboard-api }:

dockerTools.buildImage {
  name = "dashboard-api";
  tag = "latest";

  copyToRoot = [ dashboard-api ];

  config = {
    Entrypoint = [ "/bin/dashboard-api" ];
    Cmd = [ "-config" "/config/config.toml" ];
    ExposedPorts = {
      "8080/tcp" = {};
    };
    User = "65534:65534"; # nobody:nogroup
  };
}
