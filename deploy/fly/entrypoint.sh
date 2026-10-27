#!/bin/sh
# Fixed image bootstrap only. Terminal input never reaches this shell.
set -eu
umask 077

fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail 'bootstrap must start as root before dropping privileges'
[ "${FLY_APP_NAME:-}" = trill-demo ] || fail 'unexpected or missing Fly app identity'
[ -n "${FLY_MACHINE_ID:-}" ] || fail 'Fly Machine identity is required'

# Touch only the mount itself, never recursively chown existing demo state.
[ -d /data ] && [ ! -L /data ] || fail 'unsafe data mount'
mountpoint -q /data || fail '/data must be a mounted volume'
# Pairing rejects group-writable ancestors. This mount belongs only to this sandbox.
chown 10001:10001 /data
chmod 0700 /data
if [ -e /data/state ] || [ -L /data/state ]; then
    [ -d /data/state ] && [ ! -L /data/state ] || fail 'unsafe existing demo state'
    [ "$(stat -c '%u:%g:%a' /data/state)" = 10001:10001:700 ] || fail 'existing demo state ownership or mode differs'
fi
# Leave absent /data/state absent: the unprivileged app creates its ownership marker.
[ -d /run/trill ] && [ ! -L /run/trill ] || fail 'unsafe reviewer directory'
[ -f /run/trill/review-password ] && [ ! -L /run/trill/review-password ] || fail 'mounted reviewer secret is required'
[ "$(stat -c '%h' /run/trill/review-password)" = 1 ] || fail 'reviewer secret must have one link'
chmod 0700 /run/trill
chmod 0600 /run/trill/review-password
chown 10001:10001 /run/trill /run/trill/review-password
# Fly injects secrets into the environment too; exclude this copy from the service/children.
unset DEMO_REVIEW_PASSWORD_B64

# Native nftables socket UID matching works without the xt_owner kernel module.
# Recreate only our table, atomically; other UID traffic and Fly operator access
# remain untouched. No service process runs before this transaction succeeds.
nft -f - <<EOF || fail 'service egress isolation could not be installed'
add table inet trill_demo
 delete table inet trill_demo
 table inet trill_demo {
  chain service {
   ct state established,related accept
   oifname "lo" accept
   ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 240.0.0.0/4 } reject
   counter reject
  }
  chain output {
   type filter hook output priority -10; policy accept;
   meta skuid 10001 jump service
  }
 }
EOF

exec setpriv --reuid=10001 --regid=10001 --clear-groups --no-new-privs \
    --bounding-set=-all --inh-caps=-all --ambient-caps=-all \
    /usr/local/bin/trill demo serve \
    --state-dir /data/state \
    --listen 0.0.0.0:8080 \
    --fly-edge-tls \
    --address "https://${FLY_APP_NAME}.fly.dev" \
    --max-leases 2 \
    --review-password-file /run/trill/review-password \
    --retention 24h
