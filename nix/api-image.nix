{ lib, runCommand, dashboard-api, writeText }:

let
  # Incus uses Go/OCI-style architecture names.
  arch = {
    x86_64-linux = "amd64";
    aarch64-linux = "arm64";
  }.${dashboard-api.stdenv.hostPlatform.system};

  metadata = writeText "metadata.yaml" ''
    architecture: ${arch}
    creation_date: 1
    properties:
      description: Dashboard API - homelab monitoring backend
      os: linux
      release: minimal
  '';
in
runCommand "dashboard-api-incus-image.tar.gz" { } ''
  mkdir -p image/rootfs/bin image/rootfs/sbin image/rootfs/config \
           image/rootfs/secrets image/rootfs/tmp image/rootfs/etc

  cp ${metadata} image/metadata.yaml
  cp ${dashboard-api}/bin/dashboard-api image/rootfs/bin/dashboard-api

  # Incus system containers run /sbin/init as PID 1.
  # The static Go binary handles SIGTERM gracefully and does not spawn
  # children, so it is safe to run directly as PID 1.
  ln -s /bin/dashboard-api image/rootfs/sbin/init

  # Minimal /etc for DNS and user lookup
  echo 'nobody:x:65534:65534:Nobody:/:/bin/false' > image/rootfs/etc/passwd
  echo 'nogroup:x:65534:' > image/rootfs/etc/group
  echo 'hosts: files dns' > image/rootfs/etc/nsswitch.conf

  tar czf $out -C image metadata.yaml rootfs
''
