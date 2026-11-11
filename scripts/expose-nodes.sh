#!/bin/sh
# Makes this Xhaos server's node endpoint reachable at a public domain, so
# xhaos-node machines can connect from the internet (docs/nodes.md). Run once,
# after install.sh, on a Debian or Ubuntu server:
#
#   curl -fsSL -o expose-nodes.sh https://raw.githubusercontent.com/devark28/xhaos/main/scripts/expose-nodes.sh
#   sudo sh expose-nodes.sh nodes.example.com
#
# It is separate from install.sh because it changes the whole machine: it
# opens ports 80 and 443 (when ufw is on), installs Caddy, and adds a Caddy
# site that passes only /xhaos/node to Xhaos (everything else answers 404; the
# page stays behind the SSH tunnel). Caddy gets the certificate. It then sets
# PUBLIC_HOST in /etc/xhaos/install.conf and re-runs install.sh for the
# installed release, so the setting survives updates.
#
# Before changing anything it checks that the domain's DNS points at this
# machine and that nothing but Caddy uses ports 80 and 443. It never
# overwrites a Caddyfile with sites in it, and re-running it with the same
# domain changes nothing.
#
# Usage: sudo sh expose-nodes.sh DOMAIN [--port N] [--yes] [--dry-run]
#   --port N     the port Xhaos listens on (default: PORT in install.conf, 5180)
#   --yes        do not ask (installing, restarting Xhaos)
#   --dry-run    check, then print every command that would change the system
#
# Environment (for testing): XHAOS_EXPOSE_ROOT (a folder standing for /, for
# the files read and written), XHAOS_INSTALL_SH (the install.sh to run).
set -eu

REPO=devark28/xhaos
R=${XHAOS_EXPOSE_ROOT:-}
CONF=$R/etc/xhaos/install.conf
UNIT=$R/etc/systemd/system/xhaos.service
BIN=$R/usr/local/bin/xhaos
CADDYFILE=$R/etc/caddy/Caddyfile
SITES=$R/etc/caddy/sites
SITE=$SITES/xhaos-nodes.caddy
# What Caddy reads (the real paths, also under XHAOS_EXPOSE_ROOT).
IMPORT="import /etc/caddy/sites/*.caddy"
MARK="# Managed by the Xhaos installer"
DOMAIN="" PORT_ARG="" YES=no DRY=no

usage() {
  cat <<'USAGE'
Makes this Xhaos server's node endpoint (/xhaos/node) public at a domain.
  sudo sh expose-nodes.sh DOMAIN [--port N] [--yes] [--dry-run]
The domain's A (and AAAA) record must point at this server first.
  --port N     the port Xhaos listens on (default: PORT in install.conf, 5180)
  --yes        do not ask (installing, restarting Xhaos)
  --dry-run    check, then print every command that would change the system
USAGE
}

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'expose-nodes.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
  case $1 in
    --yes|-y) YES=yes ;;
    --dry-run|-n) DRY=yes ;;
    --port) shift; PORT_ARG=${1:-} ;;
    --port=*) PORT_ARG=${1#--port=} ;;
    --help|-h) usage; exit 0 ;;
    -*) die "unknown option: $1 (see --help)" ;;
    *) [ -z "$DOMAIN" ] || die "one domain only (see --help)"; DOMAIN=$1 ;;
  esac
  shift
done

# The domain goes into Caddy's and Xhaos's settings: host name characters
# only, at least two labels, none starting or ending with a hyphen.
[ -n "$DOMAIN" ] || { usage >&2; exit 2; }
DOMAIN=$(printf '%s' "$DOMAIN" | tr 'A-Z' 'a-z')
valid_domain() {
  case $1 in *[!a-z0-9.-]*) return 1 ;; esac  # also a newline: grep below reads lines
  [ ${#1} -le 253 ] || return 1
  printf '%s\n' "$1" | grep -Eq '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'
}
valid_domain "$DOMAIN" || die "not a domain name: $DOMAIN (letters, digits, hyphens and dots, like nodes.example.com)"
if [ -n "$PORT_ARG" ]; then
  case $PORT_ARG in ''|*[!0-9]*) die "--port needs a number" ;; esac
  [ "$PORT_ARG" -ge 1 ] && [ "$PORT_ARG" -le 65535 ] || die "--port must be 1-65535"
fi

# run CMD…: a command that changes the system; printed instead under --dry-run.
run() {
  if [ "$DRY" = yes ]; then say "  would run: $*"; else "$@"; fi
}

TTY=""
if [ -r /dev/tty ] && (exec </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi
# ask QUESTION: yes from --yes or the terminal; no without a terminal.
ask() {
  if [ "$YES" = yes ]; then return 0; fi
  [ -n "$TTY" ] || return 1
  while :; do
    printf '%s [y/N] ' "$1" >"$TTY"
    read -r reply <"$TTY" || reply=""
    case $reply in
      [Yy]*) return 0 ;;
      ""|[Nn]*) return 1 ;;
    esac
  done
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# put FILE: writes stdin to FILE (mode 644) unless it already holds exactly
# that; false when nothing changed.
put() {
  cat >"$TMP/put"
  if [ -f "$1" ] && cmp -s "$TMP/put" "$1"; then return 1; fi
  if [ "$DRY" = yes ]; then
    say "  would write $1:"
    sed 's/^/      /' "$TMP/put"
  else
    install -m 644 "$TMP/put" "$1"
  fi
}

# ---- checks (nothing changes before the question) ---------------------------

step "Checks"
[ "$(id -u)" -eq 0 ] || [ "$DRY" = yes ] || die "run it as root: sudo sh expose-nodes.sh $DOMAIN"
has apt-get || die "this script needs apt (Debian or Ubuntu); see docs/nodes.md to set up a reverse proxy by hand"
has systemctl || die "this script needs systemd"
for tool in curl ss; do has $tool || die "needs $tool"; done
[ -r "$CONF" ] || die "no $CONF: install Xhaos with install.sh first"
[ -f "$UNIT" ] && grep -qF "$MARK" "$UNIT" || die "$UNIT is not managed by install.sh: add -public-host $DOMAIN to Xhaos's flags yourself (docs/deploy.md)"
VERSION=$("$BIN" -version 2>/dev/null || true)
printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || die "Xhaos here is not a release (version '$VERSION'): set PUBLIC_HOST='$DOMAIN' in $CONF and re-run your install.sh yourself"

PUBLIC_HOST="" NODE_URL="" PORT=5180
# shellcheck source=/dev/null
. "$CONF"
[ -n "$PORT_ARG" ] && PORT=$PORT_ARG
if [ -n "$PUBLIC_HOST" ] && [ "$PUBLIC_HOST" != "$DOMAIN" ]; then
  die "PUBLIC_HOST is already '$PUBLIC_HOST' in $CONF: Xhaos is served at a domain already, and nodes can use it (docs/deploy.md, Nodes). For a separate node domain, set PUBLIC_HOST='$PUBLIC_HOST,$DOMAIN' and NODE_URL='https://$DOMAIN' there, re-run install.sh, and add the Caddy site from docs/nodes.md yourself."
fi
if [ -n "$NODE_URL" ] && [ "$NODE_URL" != "https://$DOMAIN" ]; then
  die "NODE_URL is already '$NODE_URL' in $CONF: empty it first if nodes should use https://$DOMAIN"
fi
say "Domain: $DOMAIN; Xhaos $VERSION on port $PORT."

# DNS: Let's Encrypt only issues a certificate when the domain reaches this
# machine, and failed attempts count against its rate limits.
resolve() { # A|AAAA
  if has dig; then
    dig +short "$1" "$DOMAIN" 2>/dev/null
  elif [ "$1" = A ]; then
    getent ahostsv4 "$DOMAIN" 2>/dev/null | awk '{print $1}'
  else
    getent ahostsv6 "$DOMAIN" 2>/dev/null | awk '{print $1}' | grep -v '^::ffff:'
  fi | tr 'A-F' 'a-f' | if [ "$1" = A ]; then grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; else grep ':'; fi | sort -u
}
# This machine's addresses: its interfaces', and how the internet sees it
# (behind a provider's NAT the public one is not on an interface).
my_addresses() {
  {
    if has ip; then ip -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1; fi
    curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true; echo
    curl -6 -fsS --max-time 5 https://api6.ipify.org 2>/dev/null || true; echo
  } | tr 'A-F' 'a-f' | grep -v '^$' | sort -u
}
# cloudflare ADDRESS: in Cloudflare's proxy ranges (cloudflare.com/ips).
cloudflare() {
  case $1 in
    2400:cb00:*|2606:4700:*|2803:f800:*|2405:b500:*|2405:8100:*|2a06:98c*|2c0f:f248:*) return 0 ;;
    *:*) return 1 ;;
  esac
  # shellcheck disable=SC2046 # split into the four parts on purpose
  set -- $(printf '%s' "$1" | tr '.' ' ')
  case $1.$2 in
    104.1[6-9]|104.2[0-7]|172.6[4-9]|172.7[01]|162.15[89]) return 0 ;;
    173.245) [ "$3" -ge 48 ] && [ "$3" -le 63 ] ;;
    103.21) [ "$3" -ge 244 ] && [ "$3" -le 247 ] ;;
    103.22) [ "$3" -ge 200 ] && [ "$3" -le 203 ] ;;
    103.31) [ "$3" -ge 4 ] && [ "$3" -le 7 ] ;;
    141.101) [ "$3" -ge 64 ] && [ "$3" -le 127 ] ;;
    108.162) [ "$3" -ge 192 ] ;;
    190.93) [ "$3" -ge 240 ] ;;
    188.114) [ "$3" -ge 96 ] && [ "$3" -le 111 ] ;;
    197.234) [ "$3" -ge 240 ] && [ "$3" -le 243 ] ;;
    198.41) [ "$3" -ge 128 ] ;;
    131.0) [ "$3" -ge 72 ] && [ "$3" -le 75 ] ;;
    *) return 1 ;;
  esac
}
A=$(resolve A)
AAAA=$(resolve AAAA)
MINE=$(my_addresses)
[ -n "$A" ] || die "$DOMAIN has no A record yet: point it at this server's public IPv4 address (${MINE:-unknown}) and wait for it to show (dig +short A $DOMAIN), then run this again"
[ -n "$MINE" ] || die "could not find this machine's addresses (ip addr, api.ipify.org)"
for a in $A $AAAA; do
  if printf '%s\n' "$MINE" | grep -qxF "$a"; then say "DNS: $a is this server."; continue; fi
  if cloudflare "$a"; then
    die "$DOMAIN points at $a, Cloudflare's proxy: set its record to \"DNS only\" (grey cloud) in Cloudflare, so nodes and Let's Encrypt reach this server; then run this again"
  fi
  die "$DOMAIN points at $a, which is not this server ($(printf '%s' "$MINE" | tr '\n' ' ')): fix its A/AAAA records (an AAAA record must point here too, or be removed), wait for the change to show, then run this again"
done

# Ports 80 and 443: Caddy's, or free. Anything else is another web server
# serving other sites; this script does not take its ports away.
LISTENERS=$(ss -Htlnp '( sport = :80 or sport = :443 )' 2>/dev/null || true)
OTHERS=$(printf '%s\n' "$LISTENERS" | grep -v '^$' | grep -v '"caddy"' || true)
if [ -n "$OTHERS" ]; then
  if [ "$(id -u)" -ne 0 ]; then
    say "Ports 80/443 are in use; not root, so which program is unknown (a real run refuses unless it is Caddy):"
    say "$OTHERS"
  else
    die "something other than Caddy listens on port 80 or 443; to keep its sites working, this script stops here. Add the site from docs/nodes.md to that web server instead:
$OTHERS"
  fi
else
  say "Ports 80/443: free, or Caddy's."
fi

# ---- what would change -------------------------------------------------------

UFW=no
if has ufw && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw status 2>/dev/null | grep -Eq '^80,443/tcp[[:space:]]+ALLOW' || UFW=yes
fi
PKG=no
has caddy || PKG=yes

# The stock Caddyfile (Debian's package, comments aside) has one site, ":80",
# serving a placeholder page for every host name on port 80.
stock() {
  [ "$(sed 's/#.*//' "$1" | tr '\t\n' '  ' | tr -s ' ' | sed 's/^ //;s/ $//')" = ':80 { root * /usr/share/caddy file_server }' ]
}
has_import() { grep -Eq '^[[:space:]]*import[[:space:]]+/etc/caddy/sites/\*\.caddy[[:space:]]*$' "$1"; }
# mentions FILE: the domain in FILE outside comments (another site for it).
mentions() { sed 's/#.*//' "$1" | grep -qiF "$DOMAIN"; }

CF=keep
if [ ! -f "$CADDYFILE" ] || stock "$CADDYFILE"; then
  has_import "$CADDYFILE" 2>/dev/null || CF=stock
elif ! has_import "$CADDYFILE"; then
  CF=import
fi
if [ -f "$CADDYFILE" ] && mentions "$CADDYFILE"; then
  die "$CADDYFILE already has a site for $DOMAIN: remove that site and run this again, or keep it (it must pass only /xhaos/node, see docs/nodes.md) and set PUBLIC_HOST='$DOMAIN' in $CONF and re-run install.sh yourself"
fi
for f in "$SITES"/*; do
  if [ -f "$f" ] && [ "$f" != "$SITE" ] && mentions "$f"; then die "$f already has a site for $DOMAIN: remove it and run this again"; fi
done

OLD_DOMAIN=""
[ -f "$SITE" ] && OLD_DOMAIN=$(sed -n 's/^\([^#[:space:]][^[:space:]]*\) {$/\1/p' "$SITE" | head -n 1)
{
  printf '# Written by expose-nodes.sh (Xhaos): the node WebSocket only.\n'
  printf '# The Xhaos page is not served here; it stays behind the SSH tunnel.\n'
  printf '%s {\n\thandle /xhaos/node {\n\t\treverse_proxy 127.0.0.1:%s\n\t}\n\trespond 404\n}\n' "$DOMAIN" "$PORT"
} >"$TMP/site"
SITE_NEW=no
[ -f "$SITE" ] && cmp -s "$TMP/site" "$SITE" || SITE_NEW=yes

CONF_NEW=no
[ "$PUBLIC_HOST" = "$DOMAIN" ] || CONF_NEW=yes
UNIT_NEW=no
sed -n 's/^ExecStart=//p' "$UNIT" | sed 's/$/ /' | grep -qF -- " -public-host $DOMAIN " || UNIT_NEW=yes

step "Plan"
CHANGES=no
plan() { say "- $*"; CHANGES=yes; }
[ "$UFW" = yes ] && plan "ufw: allow 80,443/tcp"
[ "$PKG" = yes ] && plan "apt: install caddy"
case $CF in
  stock) plan "replace the stock $CADDYFILE (its \":80\" placeholder site answers every host on port 80) with: $IMPORT" ;;
  import) plan "add one line to $CADDYFILE (its sites are kept): $IMPORT" ;;
esac
if [ "$SITE_NEW" = yes ]; then
  if [ -n "$OLD_DOMAIN" ] && [ "$OLD_DOMAIN" != "$DOMAIN" ]; then plan "replace the node site for $OLD_DOMAIN with $DOMAIN in $SITE"
  else plan "write the Caddy site $SITE: https://$DOMAIN/xhaos/node → 127.0.0.1:$PORT, everything else 404"
  fi
fi
[ "$CONF_NEW" = yes ] && plan "set PUBLIC_HOST='$DOMAIN' in $CONF"
if [ "$CONF_NEW" = yes ] || [ "$UNIT_NEW" = yes ]; then
  plan "re-run install.sh --version $VERSION --no-restart, to write that into Xhaos's service (that release's installer; it downloads the same release, checked, and changes nothing else)"
fi
if [ "$CHANGES" = no ]; then
  say "Nothing to change: $DOMAIN is set up."
elif [ "$DRY" = yes ]; then
  say "(--dry-run: nothing is changed; the commands follow.)"
elif ! ask "Go on?"; then
  [ -n "$TTY" ] || say "No terminal to ask: pass --yes to go on without asking."
  say "Nothing changed."
  exit 1
fi

# ---- firewall ----------------------------------------------------------------

step "Firewall"
if [ "$UFW" = yes ]; then run ufw allow 80,443/tcp
elif has ufw && ufw status 2>/dev/null | grep -q '^Status: active'; then say "ufw allows 80,443/tcp already."
else say "ufw is not active here (left off)."
fi
say "If your provider has a firewall of its own (a security group, a cloud firewall), allow TCP 80 and 443 there too."

# ---- Caddy ---------------------------------------------------------------------

step "Caddy"
if [ "$PKG" = yes ]; then
  run apt-get update -q
  run env DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
fi
if [ -f "$CADDYFILE" ]; then cp "$CADDYFILE" "$TMP/Caddyfile.before"; fi
if [ -f "$SITE" ]; then cp "$SITE" "$TMP/site.before"; fi
# restore: puts the Caddy files back as they were (a site Caddy refuses).
restore() {
  if [ -f "$TMP/Caddyfile.before" ]; then install -m 644 "$TMP/Caddyfile.before" "$CADDYFILE"; fi
  if [ -f "$TMP/site.before" ]; then install -m 644 "$TMP/site.before" "$SITE"; else rm -f "$SITE"; fi
}
CADDY_CHANGED=no
[ -d "$SITES" ] || run install -d -m 755 "$SITES"
if put "$SITE" <"$TMP/site"; then CADDY_CHANGED=yes; fi
case $CF in
  stock)
    [ -f "$CADDYFILE" ] && run cp "$CADDYFILE" "$CADDYFILE.stock"
    printf '# The stock ":80" placeholder site was removed by expose-nodes.sh (Xhaos)\n# (kept in Caddyfile.stock): it answered every host name on port 80.\n# Sites live in /etc/caddy/sites/, one file each.\n%s\n' "$IMPORT" | put "$CADDYFILE" && CADDY_CHANGED=yes ;;
  import)
    { cat "$CADDYFILE"; printf '\n# Sites in their own files (added by expose-nodes.sh, Xhaos).\n%s\n' "$IMPORT"; } | put "$CADDYFILE" && CADDY_CHANGED=yes
    if grep -Eq '^[[:space:]]*(:80|http://(:80)?)[[:space:]]*(\{|$)' "$CADDYFILE"; then
      say "Note: $CADDYFILE has a catch-all site on port 80 (\":80\" or \"http://\"). If Caddy cannot get $DOMAIN's certificate, give that site a host name."
    fi ;;
esac
if [ "$CADDY_CHANGED" = yes ]; then
  if [ "$DRY" = no ] && ! caddy validate --config "$CADDYFILE" --adapter caddyfile >"$TMP/validate" 2>&1; then
    restore
    tail -n 5 "$TMP/validate" >&2
    die "Caddy refused the new configuration (above); its files are back as they were"
  fi
  [ "$DRY" = yes ] && say "  would run: caddy validate --config $CADDYFILE --adapter caddyfile"
  if systemctl is-active caddy >/dev/null 2>&1; then run systemctl reload caddy
  else run systemctl enable --now caddy
  fi
  say "Caddy serves https://$DOMAIN/xhaos/node (it gets the certificate on its own)."
else
  say "Caddy is set up already."
  systemctl is-active caddy >/dev/null 2>&1 || run systemctl enable --now caddy
fi

# ---- Xhaos -----------------------------------------------------------------------

step "Xhaos"
if [ "$CONF_NEW" = yes ]; then
  if grep -q '^PUBLIC_HOST=' "$CONF"; then
    sed "s/^PUBLIC_HOST=.*/PUBLIC_HOST='$DOMAIN'/" "$CONF" | put "$CONF" || true
  else
    { cat "$CONF"; printf "PUBLIC_HOST='%s'\n" "$DOMAIN"; } | put "$CONF" || true
  fi
fi
if [ "$CONF_NEW" = yes ] || [ "$UNIT_NEW" = yes ]; then
  # install.sh writes the service from install.conf (NODE_URL and
  # TRUSTED_PROXY follow from PUBLIC_HOST). Same release, no restart (asked
  # below), and no terminal: it only asks about parts that are not installed.
  INSTALL_SH=${XHAOS_INSTALL_SH:-}
  if [ -z "$INSTALL_SH" ]; then
    INSTALL_SH=$TMP/install.sh
    run curl -fsSL -o "$INSTALL_SH" "https://raw.githubusercontent.com/$REPO/$VERSION/install.sh"
  fi
  if has setsid; then run setsid -w sh "$INSTALL_SH" --version "$VERSION" --no-restart </dev/null
  else run sh "$INSTALL_SH" --version "$VERSION" --no-restart </dev/null
  fi
fi
# running: the running Xhaos was started with -public-host DOMAIN.
running() {
  pid=$(systemctl show -p MainPID --value xhaos 2>/dev/null || true)
  [ -n "$pid" ] && [ "$pid" != 0 ] && tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null | grep -qF -- " -public-host $DOMAIN "
}
RESTARTED=yes
if ! running; then
  if [ "$DRY" = yes ]; then say "  would ask, then run: systemctl restart xhaos"
  elif ask "Restart Xhaos now, so it accepts $DOMAIN? It pauses running agents and locks every workspace until someone signs in."; then
    run systemctl restart xhaos; say "Restarted."
  else
    RESTARTED=no
    say "Not restarted: nodes get 403 until you run sudo systemctl restart xhaos."
  fi
else
  say "Xhaos accepts $DOMAIN already."
fi

# ---- check -------------------------------------------------------------------------

step "Check"
if [ "$DRY" = yes ]; then
  say "  would check https://$DOMAIN/xhaos/node (426 expected) and https://$DOMAIN/ (404 expected)"
  exit 0
fi
# status PATH: the HTTP status over HTTPS; 000 while the certificate is not
# there yet (Caddy gets it in a few seconds, at most a minute or two).
status() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "https://$DOMAIN$1" 2>/dev/null || true; }
code=000
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  code=$(status /xhaos/node)
  [ "$code" = 000 ] || break
  sleep 5
done
ok=yes
case $code in
  426) say "https://$DOMAIN/xhaos/node: 426 (Xhaos asks for a WebSocket): OK." ;;
  404) ok=no; say "https://$DOMAIN/xhaos/node: 404: Caddy's site does not pass /xhaos/node on; check $SITE and journalctl -u caddy." ;;
  403) ok=no
    if [ "$RESTARTED" = no ]; then say "https://$DOMAIN/xhaos/node: 403: expected until Xhaos restarts with -public-host $DOMAIN (sudo systemctl restart xhaos)."
    else say "https://$DOMAIN/xhaos/node: 403: Xhaos refuses the host name; check that its service has -public-host $DOMAIN (systemctl cat xhaos) and PUBLIC_HOST in $CONF."
    fi ;;
  502) ok=no; say "https://$DOMAIN/xhaos/node: 502: Caddy cannot reach Xhaos on 127.0.0.1:$PORT; is it running (systemctl status xhaos), on that port?" ;;
  000) ok=no; say "https://$DOMAIN/xhaos/node: no HTTPS answer: the certificate may not be issued yet, or ports 80/443 are closed at the provider. See journalctl -u caddy." ;;
  *) ok=no; say "https://$DOMAIN/xhaos/node: unexpected $code (426 expected)." ;;
esac
root=$(status /)
if [ "$root" = 404 ]; then say "https://$DOMAIN/: 404 (the page is not public): OK."
else ok=no; say "https://$DOMAIN/: $root, 404 expected: the domain should serve nothing but /xhaos/node; check $SITE."
fi
if [ "$ok" = yes ]; then
  step "Done"
  say "Nodes connect to https://$DOMAIN. Add one from the Nodes tab (Add node)."
else
  exit 1
fi
