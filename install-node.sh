#!/bin/sh
# Xhaos node installer: makes this Linux machine a node of an Xhaos server,
# so its agents can run commands and copy files here (docs/nodes.md).
#
#   curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/vX.Y.Z/install-node.sh | sudo sh -s -- --server https://xhaos.example.com --code XXXX-XXXX-XXXX --version vX.Y.Z
#
# (The Nodes tab gives this command with the server's own release in place of
# vX.Y.Z; a development server's uses main.)
#
# Installs xhaos-node from the GitHub release (checked against the release's
# SHA256SUMS), creates the unprivileged xhaos-node user (no sudo) and its
# folder /var/lib/xhaos-node, joins the server with the one-time code, and
# runs the node as a systemd service. Once joined, run it again without
# --code to update (a machine that has not joined needs --server and --code,
# or --no-join again).
#
# --no-join installs all of that without joining: for a machine that joins
# later, or for baking into an image (in a chroot: the service is enabled
# and waits for the node to join). It joins later with an XHAOS USB stick
# holding server= and code= (needs --usb-config), or by running this again
# with --server and --code. Its --root, --device and --unsandboxed are kept
# in /etc/xhaos-node/join.conf (root's) for that join; --root, --device or
# --sandboxed on a later run replaces them.
#
# Commands run under Landlock, or under bubblewrap on kernels without it
# (Raspberry Pi OS): there, it installs bubblewrap with apt when missing.
#
# Options:
#   --server URL       the server's public address (from the Nodes tab)
#   --code CODE        the one-time join code (from the Nodes tab)
#   --no-join          install now, join later (see above)
#   --root DIR         the folder agents work in (default /var/lib/xhaos-node/xhaos-work)
#   --device PATH      a device commands may open, e.g. /dev/video0 (repeat for more)
#   --unsandboxed      run commands unconfined: only where neither Landlock nor
#                      bubblewrap works; agents can then reach everything the
#                      xhaos-node user can
#   --sandboxed        undo a kept --unsandboxed before the node joins
#   --version vX.Y.Z   install that release instead of the latest
#   --usb-config       apply XHAOS USB sticks (see below); off unless given
#   --no-usb-config    stop applying them
#   --help
#
# With --usb-config (and systemd), plugging in a USB stick labelled XHAOS
# that holds xhaos-node.conf applies its settings (new Wi-Fi, joining again)
# and writes xhaos-node-result.txt back to it (docs/nodes.md, "USB stick
# config"). Anyone who can plug a stick in can then change the node's Wi-Fi
# and move it to their own server, so it is off by default. A re-run without
# either option keeps the current choice.
#
# Environment (mostly for testing): XHAOS_BASE_URL (where release archives
# are downloaded from).
set -eu

REPO=devark28/xhaos
BIN=/usr/local/bin/xhaos-node
USER_NAME=xhaos-node
STATE=/var/lib/xhaos-node
UNIT=/etc/systemd/system/xhaos-node.service
# What a node that has not joined yet joins with (--no-join); apply-usb reads
# it too. Root's, unlike node.json.
JOIN_CONF=/etc/xhaos-node/join.conf
ROOT_UID=0
BASE_URL=${XHAOS_BASE_URL:-https://github.com/$REPO/releases/download}
VERSION=latest SERVER="" CODE="" ROOT="" DEVICES="" DEVICES_GIVEN=no UNSANDBOXED="" USB_CONFIG="" NO_JOIN=no

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'install-node.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# as_node runs a command as the node's user.
as_node() { runuser -u "$USER_NAME" -- env HOME="$STATE" XHAOS_NODE_STATE="$STATE" "$@"; }

# confinement is the service's systemd confinement, one property per line:
# the unit and the sandbox check both use it, so the check sees what the
# service will (bubblewrap runs inside it).
confinement() {
  printf '%s\n' NoNewPrivileges=yes ProtectSystem=strict ProtectHome=read-only "ReadWritePaths=$STATE $ROOT" PrivateTmp=yes
}

# as_service runs a command as the service would: the node's user, its
# environment and confinement (systemd-run); as_node without systemd.
as_service() {
  if [ "$SYSTEMD" != yes ] || ! has systemd-run; then as_node "$@"; return; fi
  n=$#
  set -- "$@" --wait --pipe --quiet --collect -p "User=$USER_NAME" -E "HOME=$STATE" -E "XHAOS_NODE_STATE=$STATE"
  while IFS= read -r prop; do set -- "$@" -p "$prop"; done <<PROPS
$(confinement)
PROPS
  set -- "$@" --
  i=0
  while [ "$i" -lt "$n" ]; do set -- "$@" "$1"; shift; i=$((i + 1)); done # the command after the options
  systemd-run "$@" </dev/null
}

# The node's user owns its state folder, and so does any command an agent
# runs on an unsandboxed node: as root, this script never follows a link in
# there, and never chowns or chmods a path the node's user could have
# swapped for a link to a system file.

# prepare_state makes /var/lib/xhaos-node, the node user's own (its parent is
# root's, so the node cannot swap it).
prepare_state() {
  [ ! -L "$STATE" ] || die "$STATE is a symlink: refusing to touch it as root"
  mkdir -p "$STATE"
  chown -h "$USER_NAME:" "$STATE"
  chmod 0700 "$STATE"
}

# prepare_root makes the folder agents work in. Inside the state folder it is
# made by the node's user (a link planted there only reaches what that user
# can); an existing one is left as it is. Elsewhere it is the owner's own
# choice (--root): made by root and handed over without following a link.
prepare_root() {
  root=$1
  case $root in
    /*) ;;
    *) die "--root must be an absolute path: $root" ;;
  esac
  case $root in
    *[!A-Za-z0-9._/@+-]*) die "--root may hold only letters, digits and . _ / @ + -: $root" ;;
    */|*//*|*/./*|*/.|*/../*|*/..) die "--root must be a plain path (no trailing /, no . or .. parts): $root" ;;
    "$STATE") die "--root must not be $STATE itself (it holds the node's key)" ;;
  esac
  [ ! -L "$root" ] || die "$root is a symlink: refusing to touch it as root (remove it, or pass --root with a real folder)"
  case $root/ in
    "$STATE"/*/)
      if [ ! -e "$root" ]; then as_node mkdir -p "$root" || die "could not make $root as $USER_NAME"; fi
      [ -d "$root" ] || die "$root is not a folder" ;;
    *)
      if [ -e "$root" ] && [ ! -d "$root" ]; then die "$root is not a folder"; fi
      mkdir -p "$root"
      [ ! -L "$root" ] || die "$root is a symlink: refusing to touch it as root"
      chown -h "$USER_NAME:" "$root" ;;
  esac
}

# check_device accepts a device path as join does (under /dev), in the
# characters --root allows (it goes into join.conf, one per line).
check_device() {
  case $1 in
    /dev/?*) ;;
    *) die "--device $1: a device is a path under /dev" ;;
  esac
  case $1 in
    *[!A-Za-z0-9._/@+-]*|*/|*//*|*/./*|*/.|*/../*|*/..) die "--device $1: a plain path of letters, digits and . _ / @ + -" ;;
  esac
}

# load_join_conf fills what this run was not given (--root, --device,
# --unsandboxed) from join.conf, when it is root's own plain file.
load_join_conf() {
  [ -f "$JOIN_CONF" ] && [ ! -L "$JOIN_CONF" ] || return 0
  [ "$(stat -c %u "$JOIN_CONF")" = "$ROOT_UID" ] || die "$JOIN_CONF is not root's: refusing to read it"
  case $(stat -c %a "$JOIN_CONF") in
    *[2367][0-7]|*[2367]) die "$JOIN_CONF is writable by others: refusing to read it" ;;
  esac
  conf_devices=""
  while IFS= read -r line || [ -n "$line" ]; do
    case $line in
      ''|'#'*) ;;
      root=*) [ -n "$ROOT" ] || ROOT=${line#root=} ;;
      device=*) check_device "${line#device=}"; conf_devices="$conf_devices ${line#device=}" ;;
      unsandboxed=yes|unsandboxed=no) [ -n "$UNSANDBOXED" ] || UNSANDBOXED=${line#unsandboxed=} ;;
      *) die "$JOIN_CONF: unknown line: $line" ;;
    esac
  done <"$JOIN_CONF"
  [ "$DEVICES_GIVEN" = yes ] || DEVICES=$conf_devices
}

# save_join_conf keeps this run's settings for the join to come.
save_join_conf() {
  mkdir -p "${JOIN_CONF%/*}"
  {
    echo "# Managed by install-node.sh: what this node joins with when it joins later"
    echo "# (an XHAOS USB stick, or install-node.sh with --server and --code)."
    echo "root=$ROOT"
    for dev in $DEVICES; do echo "device=$dev"; done
    echo "unsandboxed=$UNSANDBOXED"
  } >"$JOIN_CONF.new"
  chmod 0644 "$JOIN_CONF.new"
  mv -f "$JOIN_CONF.new" "$JOIN_CONF"
}

# in_chroot: this runs in a chroot, as when an image is built, rather than on
# the booted system (whose PID 1 shares this root).
in_chroot() {
  pid1=$(stat -Lc %d:%i /proc/1/root 2>/dev/null) || return 0 # no /proc: not a booted system
  if has systemd-detect-virt; then systemd-detect-virt --chroot >/dev/null 2>&1; return; fi
  [ "$(stat -Lc %d:%i /)" != "$pid1" ]
}

# Tests source this file for the functions above.
# shellcheck disable=SC2317 # exit is for a run, return for a source
if [ "${XHAOS_INSTALL_NODE_FUNCTIONS:-}" = 1 ]; then return 0 2>/dev/null || exit 0; fi

usage() {
  cat <<'EOF'
install-node.sh --server URL --code CODE [options]   (as root)
install-node.sh --no-join [options]                  (install now, join later)

  --server URL       the server's public address (from the Nodes tab)
  --code CODE        the one-time join code (from the Nodes tab)
  --no-join          install without joining: for a machine or an image (built in
                     a chroot) that joins later, with an XHAOS USB stick
                     (--usb-config) or by running this again with --server and --code
  --root DIR         the folder agents work in (default /var/lib/xhaos-node/xhaos-work)
  --device PATH      a device commands may open, e.g. /dev/video0 (repeat for more)
  --unsandboxed      run commands unconfined (only where neither Landlock nor
                     bubblewrap works)
  --sandboxed        undo a kept --unsandboxed before the node joins
  --version vX.Y.Z   install that release instead of the latest
  --usb-config       apply USB sticks labelled XHAOS (new Wi-Fi or join code by USB):
                     anyone with such a stick can then change the Wi-Fi and the server
  --no-usb-config    stop applying them (the default for a new install)

Once joined, run it again without --code to update xhaos-node. --no-join
keeps --root, --device and --unsandboxed in /etc/xhaos-node/join.conf for the
join to come; --root, --device or --sandboxed on a later run replaces them.
EOF
}

while [ $# -gt 0 ]; do
  case $1 in
    --server) shift; SERVER=${1:-} ;;
    --server=*) SERVER=${1#--server=} ;;
    --code) shift; CODE=${1:-} ;;
    --code=*) CODE=${1#--code=} ;;
    --root) shift; ROOT=${1:-} ;;
    --root=*) ROOT=${1#--root=} ;;
    --device) shift; DEVICES="$DEVICES ${1:-}"; DEVICES_GIVEN=yes ;;
    --device=*) DEVICES="$DEVICES ${1#--device=}"; DEVICES_GIVEN=yes ;;
    --unsandboxed) UNSANDBOXED=yes ;;
    --sandboxed) UNSANDBOXED=no ;;
    --no-join) NO_JOIN=yes ;;
    --usb-config) USB_CONFIG=yes ;;
    --no-usb-config) USB_CONFIG=no ;;
    --version) shift; VERSION=${1:-latest} ;;
    --version=*) VERSION=${1#--version=} ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
  shift
done

[ "$(id -u)" -eq 0 ] || die "run as root (curl … | sudo sh -s -- …): it creates the xhaos-node user and its service"
[ "$(uname -s)" = Linux ] || die "nodes run on Linux"
case $(uname -m) in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m) (xhaos-node is built for amd64 and arm64; on a Raspberry Pi use the 64-bit OS)" ;;
esac
for tool in curl tar sha256sum; do has $tool || die "needs $tool"; done
RETRY="--retry 4 --retry-delay 3"
curl --help all 2>/dev/null | grep -q -- --retry-all-errors && RETRY="$RETRY --retry-all-errors"
# shellcheck disable=SC2086
dl() { curl $RETRY "$@"; }
for dev in $DEVICES; do check_device "$dev"; done
# IMAGE: an image being built in a chroot: the units are written and enabled
# but nothing is started or reloaded (a /run bound from the host would reach
# the host's systemd), and the sandbox is not checked (this kernel is the
# build host's). It joins on its own machine, so it needs --no-join.
IMAGE=no SYSTEMD=no
if in_chroot; then
  IMAGE=yes
  [ "$NO_JOIN" = yes ] || die "this runs in a chroot (an image being built?): pass --no-join; the node joins on its own machine"
elif [ -d /run/systemd/system ] && has systemctl; then
  SYSTEMD=yes
fi

[ "$NO_JOIN" = no ] || { [ -z "$CODE" ] && [ -z "$SERVER" ]; } || die "--no-join takes no --server or --code (they go on the USB stick, or on a later run)"
JOINED=no
[ -f "$STATE/node.json" ] && JOINED=yes
# Not joined yet: what an earlier --no-join kept, for what this run lacks.
[ "$JOINED" = yes ] || load_join_conf
[ -n "$UNSANDBOXED" ] || UNSANDBOXED=no
if [ "$JOINED" = no ] && [ "$NO_JOIN" = no ] && { [ -z "$SERVER" ] || [ -z "$CODE" ]; }; then
  die "this machine has not joined a server yet: pass --server and --code (the Nodes tab shows the whole command), or --no-join to install now and join later"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# ---- the release -------------------------------------------------------------

step "xhaos-node release"
if [ "$VERSION" = latest ]; then
  VERSION=$(dl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true)
  if [ -z "$VERSION" ]; then
    VERSION=$(dl -fsSI -o /dev/null -w '%{redirect_url}' "https://github.com/$REPO/releases/latest" 2>/dev/null | sed -n 's#.*/releases/tag/##p' || true)
  fi
  [ -n "$VERSION" ] || die "could not find the latest release on GitHub; try again, or pass --version vX.Y.Z"
fi
NAME="xhaos-node-$VERSION-linux-$ARCH"
say "Downloading $NAME from $BASE_URL/$VERSION/"
dl -fsSL -o "$TMP/$NAME.tar.gz" "$BASE_URL/$VERSION/$NAME.tar.gz" || die "download failed: $NAME.tar.gz (releases made before nodes have no xhaos-node; pass --version of one that does)"
dl -fsSL -o "$TMP/SHA256SUMS" "$BASE_URL/$VERSION/SHA256SUMS" || die "download failed: SHA256SUMS"
(cd "$TMP" && grep " \(\./\)\{0,1\}$NAME.tar.gz\$" SHA256SUMS | sed "s# \./# #" | sha256sum -c --status) || die "checksum mismatch: $NAME.tar.gz does not match SHA256SUMS"
say "Checksum OK."
tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
install -m 0755 "$TMP/$NAME/xhaos-node" "$BIN.new"
mv -f "$BIN.new" "$BIN"
say "Installed $BIN ($("$BIN" version))."

# ---- the user ----------------------------------------------------------------

step "The xhaos-node user"
if ! id "$USER_NAME" >/dev/null 2>&1; then
  NOLOGIN=/usr/sbin/nologin
  [ -x "$NOLOGIN" ] || NOLOGIN=/bin/false
  useradd --system --home-dir "$STATE" --create-home --shell "$NOLOGIN" "$USER_NAME"
  say "Created the system user $USER_NAME (no password, no sudo)."
fi
prepare_state
if [ -z "$ROOT" ] && [ -f "$UNIT" ]; then
  # An update keeps the folder the service was given (the unit is root's;
  # node.json is the node user's and is not read here): the second path of
  # the unit's own ReadWritePaths line (drop-ins are not read).
  ROOT=$(sed -n "s#^ReadWritePaths=$STATE \([^ ]*\).*#\1#p" "$UNIT" | head -n 1)
fi
[ -n "$ROOT" ] || ROOT=$STATE/xhaos-work
prepare_root "$ROOT"

# A device is opened by the user's group too (the sandbox allows it; the
# file's permissions still decide).
for dev in $DEVICES; do
  if [ ! -e "$dev" ]; then
    say "warning: $dev does not exist now; it is allowed when it appears"
    # The group such devices get from udev, so the node can open it then.
    case $dev in
      /dev/video*|/dev/media*) group=video ;;
      /dev/ttyUSB*|/dev/ttyACM*|/dev/ttyAMA*|/dev/serial*) group=dialout ;;
      /dev/dri/*|/dev/dri) group=render ;;
      *) group="" ;;
    esac
    if [ -n "$group" ] && getent group "$group" >/dev/null 2>&1; then
      usermod -aG "$group" "$USER_NAME" && say "Added $USER_NAME to the $group group (what $dev usually belongs to)."
    fi
  else
    group=$(stat -c %G "$dev")
    if [ "$group" != root ] && [ "$group" != UNKNOWN ]; then
      usermod -aG "$group" "$USER_NAME" && say "Added $USER_NAME to the $group group (for $dev)."
    fi
  fi
done

# ---- the sandbox -----------------------------------------------------------------

# Landlock when the kernel has it, else bubblewrap (installed here when
# missing). Never fatal: without either, the node joins and refuses commands.
# (Releases before the bubblewrap fallback have no sandbox-check.)
if [ "$UNSANDBOXED" = no ] && [ "$IMAGE" = yes ]; then
  # The image's own kernel is not running here; Raspberry Pi kernels have no
  # Landlock, so the image gets bubblewrap (about 150 kB).
  step "The sandbox"
  if ! has bwrap && has apt-get; then
    say "Building an image: installing bubblewrap (for kernels without Landlock, such as Raspberry Pi OS's)."
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y --no-install-recommends bubblewrap >/dev/null 2>&1 ||
      { apt-get update >/dev/null 2>&1 && apt-get install -y --no-install-recommends bubblewrap >/dev/null 2>&1; } ||
      say "warning: apt-get install bubblewrap failed; add the bubblewrap package to the image"
  elif ! has bwrap; then
    say "Building an image without apt: add bubblewrap to it (kernels without Landlock need it to run commands)."
  fi
  say "The sandbox is picked when the node starts on its own kernel (Landlock, else bubblewrap)."
elif [ "$UNSANDBOXED" = no ] && "$BIN" help 2>/dev/null | grep -q sandbox-check; then
  step "The sandbox"
  if ! as_node "$BIN" sandbox-check --sandbox landlock >/dev/null 2>&1 && ! has bwrap; then
    if has apt-get; then
      say "This kernel has no Landlock; installing bubblewrap so commands run sandboxed."
      export DEBIAN_FRONTEND=noninteractive
      apt-get install -y --no-install-recommends bubblewrap >/dev/null 2>&1 ||
        { apt-get update >/dev/null 2>&1 && apt-get install -y --no-install-recommends bubblewrap >/dev/null 2>&1; } ||
        say "warning: apt-get install bubblewrap failed; install it yourself, then: systemctl restart xhaos-node"
    else
      say "This kernel has no Landlock and bwrap is missing: install bubblewrap with your package manager, then re-run this."
    fi
  fi
  # As the service runs it (its confinement could stop bubblewrap).
  if line=$(as_service "$BIN" sandbox-check 2>&1); then
    say "Commands run under: $line"
  else
    say "warning: $line"
  fi
fi

# ---- joining -------------------------------------------------------------------

if [ "$JOINED" = yes ]; then
  [ -z "$CODE" ] || say "This machine has joined a server already; the code is not used (xhaos-node leave first to join another)."
elif [ "$NO_JOIN" = yes ]; then
  save_join_conf
  say "Not joining now (--no-join). It joins with: root $ROOT${DEVICES:+, devices$DEVICES}$( [ "$UNSANDBOXED" = no ] || echo ", unsandboxed") (kept in $JOIN_CONF)."
else
  save_join_conf
  step "Joining $SERVER"
  set -- join --server "$SERVER" --code "$CODE" --root "$ROOT"
  for dev in $DEVICES; do set -- "$@" --device "$dev"; done
  [ "$UNSANDBOXED" = yes ] && set -- "$@" --unsandboxed
  as_node "$BIN" "$@" || die "joining failed (see above); make a new code on the Nodes tab if this one is used or expired"
fi

# ---- the service -----------------------------------------------------------------

step "The service"
if [ "$SYSTEMD" = no ] && { [ "$IMAGE" = no ] || ! has systemctl; }; then
  [ "$JOINED" = yes ] || [ "$NO_JOIN" = no ] || { say "No systemd here. Join with: runuser -u $USER_NAME -- env HOME=$STATE $BIN join --server URL --code CODE --root $ROOT$(for d in $DEVICES; do printf ' --device %s' "$d"; done)$( [ "$UNSANDBOXED" = no ] || echo " --unsandboxed"), then start it as below."; }
  say "No systemd here: start the node yourself, as $USER_NAME:"
  say "  runuser -u $USER_NAME -- env HOME=$STATE $BIN run"
  exit 0
fi
cat >"$TMP/unit" <<UNIT
# Managed by install-node.sh; re-run it to update.
[Unit]
Description=Xhaos node (docs/nodes.md)
Wants=network-online.target
After=network-online.target
# Not joined yet (--no-join): the service waits until the node joins.
ConditionPathExists=$STATE/node.json

[Service]
User=$USER_NAME
Environment=HOME=$STATE XHAOS_NODE_STATE=$STATE
ExecStart=$BIN run
Restart=always
RestartSec=10
# 3: the server refused this node (it was removed); retrying will not help.
RestartPreventExitStatus=3
$(confinement)

[Install]
WantedBy=multi-user.target
UNIT
# reload: tell a running systemd its units changed (not in an image).
reload() {
  [ "$SYSTEMD" = yes ] || return 0
  systemctl daemon-reload
  [ "${1:-}" != udev ] || udevadm control --reload-rules 2>/dev/null || true
}
if ! cmp -s "$TMP/unit" "$UNIT" 2>/dev/null; then
  install -m 0644 "$TMP/unit" "$UNIT"
  reload
fi
systemctl enable xhaos-node >/dev/null 2>&1 || true

# An XHAOS stick: udev starts a one-shot root service for it (the node itself
# stays unprivileged).
USB_RULE=/etc/udev/rules.d/90-xhaos-node-usb.rules
USB_UNIT=/etc/systemd/system/xhaos-node-usb@.service
if [ -z "$USB_CONFIG" ]; then # a re-run keeps the choice; a new install has it off
  if [ -e "$USB_UNIT" ]; then USB_CONFIG=yes; else USB_CONFIG=no; fi
fi
if [ "$USB_CONFIG" = yes ]; then
  cat >"$TMP/rule" <<'RULE'
# Managed by install-node.sh: a stick labelled XHAOS applies its xhaos-node.conf (docs/nodes.md).
ACTION=="add", SUBSYSTEM=="block", ENV{ID_FS_LABEL}=="XHAOS|xhaos", ENV{ID_FS_TYPE}=="vfat|exfat", TAG+="systemd", ENV{SYSTEMD_WANTS}+="xhaos-node-usb@%k.service"
RULE
  cat >"$TMP/usbunit" <<USBUNIT
# Managed by install-node.sh; re-run it with --no-usb-config to remove.
[Unit]
Description=Apply the XHAOS USB stick %I to this node (docs/nodes.md)
BindsTo=dev-%i.device
After=dev-%i.device NetworkManager.service

[Service]
Type=oneshot
Environment=XHAOS_NODE_STATE=$STATE
ExecStart=$BIN apply-usb /dev/%I
TimeoutStartSec=5min
USBUNIT
  changed=no
  cmp -s "$TMP/rule" "$USB_RULE" 2>/dev/null || { install -m 0644 "$TMP/rule" "$USB_RULE"; changed=yes; }
  cmp -s "$TMP/usbunit" "$USB_UNIT" 2>/dev/null || { install -m 0644 "$TMP/usbunit" "$USB_UNIT"; changed=yes; }
  [ "$changed" = no ] || reload udev
  say "A USB stick labelled XHAOS with xhaos-node.conf now applies its settings (--no-usb-config turns this off)."
elif [ -e "$USB_RULE" ] || [ -e "$USB_UNIT" ]; then
  rm -f "$USB_RULE" "$USB_UNIT"
  reload udev
  say "USB stick settings are off (--no-usb-config)."
else
  say "USB stick settings are off (--usb-config turns them on)."
fi
if [ "$JOINED" = no ] && [ "$NO_JOIN" = yes ]; then
  # node.json is not there: the service starts once the node joins.
  if [ "$USB_CONFIG" = yes ]; then
    say "Not joined yet. To join, plug in a USB stick labelled XHAOS whose xhaos-node.conf has server= and code= (the Nodes tab's USB stick file), or run this again with --server and --code."
  else
    say "Not joined yet. To join, run this again with --server and --code (add --usb-config here to join with an XHAOS USB stick instead)."
  fi
  [ "$IMAGE" = no ] || say "Enabled in the image: xhaos-node starts on the first boot after it joins."
  exit 0
fi
systemctl restart xhaos-node
sleep 2
if systemctl is-active --quiet xhaos-node; then
  say "xhaos-node is running. The node's card on the Nodes tab turns online."
  # What the service itself picked (its systemd settings could differ from
  # the check above).
  sb=$(journalctl -u xhaos-node --since -15s -o cat --no-pager 2>/dev/null | grep '^xhaos-node: sandbox:' | tail -n 1 || true)
  [ -z "$sb" ] || say "The service says: ${sb#xhaos-node: }"
else
  say "xhaos-node did not stay up; see: journalctl -u xhaos-node -n 50"
  exit 1
fi
