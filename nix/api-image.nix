{ lib, runCommand, dashboard-api, busybox, cacert, writeText }:

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

  # Init script: obtain an IPv4 lease via DHCP, then exec the Go binary.
  # udhcpc -n = exit if no lease obtained, -q = quit after obtaining lease,
  # -s = dispatcher script that configures the interface and resolv.conf.
  # -x hostname:$(hostname) = tell DHCP server our hostname so it goes into DNS
  init = writeText "init" ''
#!/bin/sh
/bin/udhcpc -i eth0 -n -q -s /etc/udhcpc.script -x hostname:$(/bin/hostname)
exec /bin/dashboard-api
  '';
in
runCommand "dashboard-api-incus-image.tar.gz" { } ''
  mkdir -p image/rootfs/bin image/rootfs/sbin image/rootfs/config \
           image/rootfs/secrets image/rootfs/tmp image/rootfs/etc/ssl/certs

  cp ${metadata} image/metadata.yaml
  cp ${dashboard-api}/bin/dashboard-api image/rootfs/bin/dashboard-api

  # Include busybox for shell and udhcpc (DHCP client).
  # Busybox is a single static binary; symlink common applets.
  cp ${busybox}/bin/busybox image/rootfs/bin/busybox
  for cmd in sh ip ifconfig udhcpc logger awk cat hostname grep; do
    ln -s busybox image/rootfs/bin/$cmd
  done

  # Incus system containers run /sbin/init as PID 1.
  # The init script obtains an IPv4 DHCP lease (so Incus DNS registers
  # the container hostname) and then execs the Go binary as PID 1.
  cp ${init} image/rootfs/sbin/init
  chmod +x image/rootfs/sbin/init

  # udhcpc dispatcher script (configures interface, routes, resolv.conf)
  cp ${busybox}/default.script image/rootfs/etc/udhcpc.script
  # Patch Nix store paths to use /bin (where our busybox symlinks live)
  substituteInPlace image/rootfs/etc/udhcpc.script \
    --replace-fail '${busybox}/bin/' '/bin/'
  chmod +x image/rootfs/etc/udhcpc.script

  # CA certificates for TLS connections (ntfy.sh, Incus metrics endpoint)
  cp ${cacert}/etc/ssl/certs/ca-bundle.crt image/rootfs/etc/ssl/certs/ca-certificates.crt

  # Minimal /etc for DNS and user lookup
  echo 'nobody:x:65534:65534:Nobody:/:/bin/false' > image/rootfs/etc/passwd
  echo 'nogroup:x:65534:' > image/rootfs/etc/group
  echo 'hosts: files dns' > image/rootfs/etc/nsswitch.conf

  tar czf $out -C image metadata.yaml rootfs
''
