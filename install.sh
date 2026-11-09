#!/bin/sh
# Xhaos installer and updater, for Linux servers with systemd (Debian/Ubuntu
# for the optional parts).
#
#   curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/main/install.sh | sh
#
# First run: installs the release binary (checksum verified), creates the
# xhaos user, its data folder and a systemd service, and offers optional
# parts (build tools and languages, browsers, rootless containers, speech
# dictation, the optional command classifier model), each with what it is,
# why, and where it comes from. Later runs update the binary and ask before
# restarting.
#
# Options:
#   --yes              answer yes to every question (unattended), except
#                      adding the classifier model (needs --with classifier)
#   --with a,b,c       install these parts without asking: devtools, flutter,
#                      browsers, podman, dictation, classifier (also to update
#                      them). classifier is the optional command model, no by
#                      default; the built-in command list needs no part.
#   --without a,b      skip these parts, without asking
#   --no-restart       never restart a running Xhaos
#   --version vX.Y.Z   install that release instead of the latest
#   --help
#
# Environment (mostly for testing): XHAOS_VERSION, XHAOS_BASE_URL (where
# release archives are downloaded from), XHAOS_MODEL_BASE (the dictation
# model's folder), XHAOS_LLAMA_BASE (where the llama.cpp release tarball is
# downloaded from), XHAOS_CLASSIFIER_MODEL_BASE (the classifier model's folder).
# Checksums are pinned in this file: a download from elsewhere must match them.
set -eu

REPO=devark28/xhaos
BIN=/usr/local/bin/xhaos
HOME_DIR=/var/lib/xhaos
DATA=$HOME_DIR/data
CONF_DIR=/etc/xhaos
CONF=$CONF_DIR/install.conf
UNIT=/etc/systemd/system/xhaos.service
MARK="# Managed by the Xhaos installer (install.sh). Change settings in $CONF and re-run it."
BASE_URL=${XHAOS_BASE_URL:-https://github.com/$REPO/releases/download}
VERSION=${XHAOS_VERSION:-latest}
YES=no WITH="" WITHOUT="" RESTART=yes

usage() {
  cat <<'USAGE'
Xhaos installer and updater.
  curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/main/install.sh | sh
Options:
  --yes              answer yes to every question (unattended), except
                     adding the classifier model (needs --with classifier)
  --with a,b,c       install or update these parts without asking:
                     devtools, flutter, browsers, podman, dictation,
                     classifier (the optional command model; no by
                     default, the built-in command list needs no part)
  --without a,b      skip these parts, without asking
  --no-restart       never restart a running Xhaos
  --version vX.Y.Z   install that release instead of the latest
USAGE
}

# ---- small helpers -----------------------------------------------------------

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
  case $1 in
    --yes|-y) YES=yes ;;
    --with) shift; WITH=",${1:-},";;
    --with=*) WITH=",${1#--with=},";;
    --without) shift; WITHOUT=",${1:-},";;
    --without=*) WITHOUT=",${1#--without=},";;
    --no-restart) RESTART=no ;;
    --version) shift; VERSION=${1:-latest} ;;
    --version=*) VERSION=${1#--version=} ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
  shift
done

# Questions read the terminal, not stdin (which is this script under curl | sh).
TTY=""
if [ -r /dev/tty ] && (exec </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi

# ask QUESTION DEFAULT(y|n): yes or no, from --yes, the terminal, or the default.
ask() {
  if [ "$YES" = yes ]; then return 0; fi
  if [ -z "$TTY" ]; then [ "$2" = y ]; return; fi
  if [ "$2" = y ]; then hint="[Y/n]"; else hint="[y/N]"; fi
  while :; do
    printf '%s %s ' "$1" "$hint" >"$TTY"
    read -r reply <"$TTY" || reply=""
    case $reply in
      "") [ "$2" = y ]; return ;;
      [Yy]*) return 0 ;;
      [Nn]*) return 1 ;;
    esac
  done
}

# prompt QUESTION DEFAULT: a line of text (the default when unattended).
prompt() {
  if [ "$YES" = yes ] || [ -z "$TTY" ]; then printf '%s' "$2"; return; fi
  printf '%s [%s] ' "$1" "$2" >"$TTY"
  read -r reply <"$TTY" || reply=""
  printf '%s' "${reply:-$2}"
}

# want PART: whether to install an optional part (asked with its description).
want() {
  case $WITH in *",$1,"*) return 0 ;; esac
  printf '\n%s\n' "$2"
  ask "  Install it?" "$3"
}

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else has sudo || die "run as root, or install sudo"; SUDO=sudo; fi
run() { $SUDO "$@"; }
# Xhaos's folders are private to the xhaos user (700): look inside them as
# root, not as the person running the installer.
xtest() { $SUDO test "$@"; }
xsum() { $SUDO sha256sum "$1" 2>/dev/null | cut -c1-64; }
# as_user USER CMD…: a command as another user (runuser as root, else sudo -u).
as_user() { u=$1; shift; if [ -z "$SUDO" ]; then runuser -u "$u" -- "$@"; else sudo -u "$u" "$@"; fi; }
# The person installing (their user gets the per-user parts: Flutter, Podman).
ME=${SUDO_USER:-$(id -un)}
[ "$ME" = root ] && ME=""

[ "$(uname -s)" = Linux ] || die "Xhaos servers run on Linux"
case $(uname -m) in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m) (releases are built for amd64 and arm64)" ;;
esac
for tool in curl tar sha256sum; do has $tool || die "needs $tool"; done
# Retry downloads (GitHub can answer 5xx for a moment); --retry-all-errors
# needs curl 7.71+, so older ones retry only what curl calls transient.
RETRY="--retry 4 --retry-delay 3"
curl --help all 2>/dev/null | grep -q -- --retry-all-errors && RETRY="$RETRY --retry-all-errors"
# dl: curl with those retries ($RETRY is meant to split into its options).
# shellcheck disable=SC2086
dl() { curl $RETRY "$@"; }
SYSTEMD=no
[ -d /run/systemd/system ] && has systemctl && SYSTEMD=yes
APT=no
has apt-get && APT=yes

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

DICT=$HOME_DIR/dictation
MODEL_DIR=$DICT/models/whisper-large-v3-turbo
# The command classifier model (optional, no by default): llama.cpp's
# llama-server and a small model, as a service of its own on the loopback
# (nothing else here uses port 8189). Xhaos's built-in list of build, test and
# lint commands needs none of it.
CLS=$HOME_DIR/classifier
LLAMA_TAG=b12110
LLAMA_DIR=$CLS/llama-$LLAMA_TAG
LLAMA_SUMS=$CLS/llama-$LLAMA_TAG.sha256sums
CLS_MODEL_FILE=qwen2.5-coder-3b-instruct-q4_k_m.gguf
CLS_MODEL=$CLS/models/$CLS_MODEL_FILE
CLS_PORT=8189
CLS_UNIT=/etc/systemd/system/xhaos-classifier.service

# Settings kept between runs (written back at the end).
PROJECTS="" PUBLIC_HOST="" TRUSTED_PROXY="" LISTEN=127.0.0.1 PORT=5180
DEVTOOLS=no FLUTTER=no BROWSERS=no PODMAN=no DICTATION=no CLASSIFIER=no
if [ -r "$CONF" ]; then
  # shellcheck source=/dev/null
  . "$CONF"
else
  # No settings yet (a first install, or a server set up by hand): what is
  # already here counts as installed, so the service keeps using it.
  [ -x /opt/rust/cargo/bin/rustup ] && DEVTOOLS=yes
  xtest -d "$DATA/sandbox/flutter/.git" && FLUTTER=yes
  has google-chrome && has firefox && BROWSERS=yes
  has podman && has docker-compose && PODMAN=yes
  xtest -x "$DICT/venv/bin/python" && xtest -f "$MODEL_DIR/model.bin" && DICTATION=yes
  xtest -x "$LLAMA_DIR/llama-server" && xtest -f "$CLS_MODEL" && CLASSIFIER=yes
fi

# ---- the release -----------------------------------------------------------

step "Xhaos release"
if [ "$VERSION" = latest ]; then
  # The API (retried: it can answer 5xx for a moment), else the redirect of
  # the plain "latest release" page, which names the tag.
  VERSION=$(dl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true)
  if [ -z "$VERSION" ]; then
    VERSION=$(dl -fsSI -o /dev/null -w '%{redirect_url}' "https://github.com/$REPO/releases/latest" 2>/dev/null | sed -n 's#.*/releases/tag/##p' || true)
  fi
  [ -n "$VERSION" ] || die "could not find the latest release on GitHub; try again, or pass --version vX.Y.Z"
fi
NAME="xhaos-$VERSION-linux-$ARCH"
say "Downloading $NAME from $BASE_URL/$VERSION/"
dl -fsSL -o "$TMP/$NAME.tar.gz" "$BASE_URL/$VERSION/$NAME.tar.gz" || die "download failed: $NAME.tar.gz"
dl -fsSL -o "$TMP/SHA256SUMS" "$BASE_URL/$VERSION/SHA256SUMS" || die "download failed: SHA256SUMS"
(cd "$TMP" && grep " \(\./\)\{0,1\}$NAME.tar.gz\$" SHA256SUMS | sed "s# \./# #" | sha256sum -c --status) || die "checksum mismatch: $NAME.tar.gz does not match SHA256SUMS"
say "Checksum OK."
tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
NEW="$TMP/$NAME"
[ -x "$NEW/xhaos" ] || die "the archive has no xhaos binary"
OLD_VERSION=""
[ -x "$BIN" ] && OLD_VERSION=$("$BIN" -version 2>/dev/null || echo unknown)
NEW_VERSION=$("$NEW/xhaos" -version)
FIRST=yes
[ -n "$OLD_VERSION" ] && FIRST=no
if [ "$FIRST" = yes ]; then say "Installing Xhaos $NEW_VERSION."
elif [ "$OLD_VERSION" = "$NEW_VERSION" ]; then say "Xhaos $NEW_VERSION is already installed."
else say "Updating Xhaos $OLD_VERSION → $NEW_VERSION."
fi
CHANGED=no
if [ "$OLD_VERSION" != "$NEW_VERSION" ] || ! cmp -s "$NEW/xhaos" "$BIN"; then CHANGED=yes; fi
[ "$CHANGED" = yes ] && run install -m 755 "$NEW/xhaos" "$BIN"

# ---- the xhaos user, its folders, its git identity -------------------------------

step "The xhaos user and its data"
if ! id xhaos >/dev/null 2>&1; then
  run useradd --system --create-home --home-dir "$HOME_DIR" --shell /bin/bash xhaos
  say "Created the xhaos user (home $HOME_DIR)."
fi
run install -d -m 700 -o xhaos -g xhaos "$HOME_DIR" "$DATA"
if [ -z "$PROJECTS" ]; then
  # An existing hand-made unit tells where projects live; else ask.
  found=""
  [ -r "$UNIT" ] && found=$(sed -n 's/^ReadWritePaths=//p' "$UNIT" | tr ' ' '\n' | grep -v "^$HOME_DIR\$" | tr '\n' ' ' | sed 's/ $//')
  PROJECTS=$(prompt "Where will your projects live (folders, space-separated)?" "${found:-/srv/projects}")
fi
for dir in $PROJECTS; do
  if [ ! -d "$dir" ]; then run install -d -m 755 -o xhaos -g xhaos "$dir"; say "Created $dir for projects."; fi
done
if [ -z "$(as_user xhaos git config --global user.name 2>/dev/null || true)" ] && has git; then
  gname=$(prompt "Git name for the agents' commits?" "Xhaos agents")
  gmail=$(prompt "Git email for the agents' commits?" "agents@$(hostname -f 2>/dev/null || hostname)")
  as_user xhaos git config --global user.name "$gname"
  as_user xhaos git config --global user.email "$gmail"
fi
[ -n "$PUBLIC_HOST" ] || [ "$FIRST" = no ] || PUBLIC_HOST=$(prompt "A domain name a reverse proxy serves Xhaos under (empty: none yet)?" "")
[ -n "$PUBLIC_HOST" ] && [ -z "$TRUSTED_PROXY" ] && TRUSTED_PROXY="127.0.0.1,::1"

# ---- optional parts --------------------------------------------------------------

apt_install() { run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$@" >/dev/null; }
apt_updated=no
apt_update() { [ "$apt_updated" = yes ] || { run apt-get update -q >/dev/null; apt_updated=yes; }; }
needs_apt() { [ "$APT" = yes ] || { say "  Skipped: this part needs apt (Debian or Ubuntu)."; return 1; }; }

install_devtools() {
  needs_apt || return 0
  apt_update; apt_install build-essential pkg-config cmake libssl-dev unzip jq git
  say "  Go (latest stable, go.dev) in /usr/local/go"
  gov=$(curl -fsSL 'https://go.dev/dl/?mode=json' | sed -n 's/.*"version": *"\(go[0-9.]*\)".*/\1/p' | head -n 1)
  [ -n "$gov" ] || die "could not read the latest Go version from go.dev"
  goarch=$ARCH
  curl -fsSL -o "$TMP/go.tgz" "https://go.dev/dl/$gov.linux-$goarch.tar.gz"
  run rm -rf /usr/local/go && run tar -C /usr/local -xzf "$TMP/go.tgz" && run ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/
  say "  Node.js (current LTS, nodejs.org, through n) in /usr/local"
  run curl -fsSL -o /usr/local/bin/n https://raw.githubusercontent.com/tj/n/master/bin/n && run chmod 755 /usr/local/bin/n
  run n lts >/dev/null
  say "  Rust (rustup, rust-lang.org) in /opt/rust"
  if [ -x /opt/rust/cargo/bin/rustup ]; then run env RUSTUP_HOME=/opt/rust/rustup CARGO_HOME=/opt/rust/cargo /opt/rust/cargo/bin/rustup update >/dev/null
  else curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | run env RUSTUP_HOME=/opt/rust/rustup CARGO_HOME=/opt/rust/cargo sh -s -- -y -q --no-modify-path --profile minimal -c rustfmt -c clippy
  fi
  run sh -c 'ln -sf /opt/rust/cargo/bin/* /usr/local/bin/ && chmod -R a+rX /opt/rust'
  printf 'export RUSTUP_HOME=/opt/rust/rustup\n' | run tee /etc/profile.d/rustup.sh >/dev/null
}

install_flutter() {
  needs_apt || return 0
  apt_update; apt_install xz-utils zip unzip libglu1-mesa git
  # The flutter tool writes into its own folder, so agents get a copy in the
  # sandbox's shared cache (writable by their commands), you one of your own.
  sb=$DATA/sandbox
  run install -d -m 700 -o xhaos -g xhaos "$sb" "$sb/home"
  flutter_at() {
    if xtest -d "$2/.git"; then as_user "$1" env HOME="$3" "$2/bin/flutter" upgrade >/dev/null
    else as_user "$1" git clone -q --depth 1 -b stable https://github.com/flutter/flutter.git "$2"
    fi
    as_user "$1" env HOME="$3" "$2/bin/flutter" --disable-analytics >/dev/null 2>&1 || true
    as_user "$1" env HOME="$3" "$2/bin/flutter" precache --universal >/dev/null
  }
  say "  Flutter (stable, github.com/flutter/flutter) for the agents"
  flutter_at xhaos "$sb/flutter" "$sb/home"
  if [ -n "$ME" ]; then
    mh=$(getent passwd "$ME" | cut -d: -f6)
    say "  …and for you ($ME) in ~/.local/flutter"
    as_user "$ME" mkdir -p "$mh/.local"
    flutter_at "$ME" "$mh/.local/flutter" "$mh"
    # shellcheck disable=SC2016 # written literally: the shell expands it at login
    grep -q '.local/flutter/bin' "$mh/.profile" 2>/dev/null || printf 'export PATH="$HOME/.local/flutter/bin:$PATH"\n' | as_user "$ME" tee -a "$mh/.profile" >/dev/null
  fi
}

install_browsers() {
  needs_apt || return 0
  [ "$ARCH" = amd64 ] || { say "  Skipped: Google Chrome is only built for amd64."; return 0; }
  run install -d -m 755 /etc/apt/keyrings
  curl -fsSL https://dl.google.com/linux/linux_signing_key.pub | run gpg --dearmor --yes -o /etc/apt/keyrings/google-chrome.gpg
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" | run tee /etc/apt/sources.list.d/google-chrome.list >/dev/null
  run curl -fsSL -o /etc/apt/keyrings/packages.mozilla.org.asc https://packages.mozilla.org/apt/repo-signing-key.gpg
  echo "deb [signed-by=/etc/apt/keyrings/packages.mozilla.org.asc] https://packages.mozilla.org/apt mozilla main" | run tee /etc/apt/sources.list.d/mozilla.list >/dev/null
  printf 'Package: *\nPin: origin packages.mozilla.org\nPin-Priority: 1000\n' | run tee /etc/apt/preferences.d/mozilla >/dev/null
  apt_updated=no; apt_update
  apt_install google-chrome-stable firefox fonts-liberation fonts-noto-color-emoji xvfb
  if has npx; then say "  Playwright's system libraries"; run npx -y playwright@latest install-deps chromium firefox >/dev/null
  else say "  (Playwright's libraries need Node.js: install the dev tools, then re-run with --with browsers.)"
  fi
  gv=$(curl -fsSL https://api.github.com/repos/mozilla/geckodriver/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  curl -fsSL "https://github.com/mozilla/geckodriver/releases/download/$gv/geckodriver-$gv-linux64.tar.gz" | tar -xz -C "$TMP" geckodriver
  run install -m 755 "$TMP/geckodriver" /usr/local/bin/geckodriver
}

install_podman() {
  needs_apt || return 0
  [ -n "$ME" ] || { say "  Skipped: rootless Podman is set up for the user running the installer (run it as yourself, with sudo)."; return 0; }
  apt_update; apt_install podman uidmap passt slirp4netns fuse-overlayfs
  grep -q "^$ME:" /etc/subuid || run usermod --add-subuids 100000-165535 "$ME"
  grep -q "^$ME:" /etc/subgid || run usermod --add-subgids 100000-165535 "$ME"
  run loginctl enable-linger "$ME"
  uid=$(id -u "$ME")
  for _ in 1 2 3 4 5 6 7 8 9 10; do [ -S "/run/user/$uid/bus" ] && break; sleep 0.5; done
  as_user "$ME" env XDG_RUNTIME_DIR="/run/user/$uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user enable --now podman.socket >/dev/null 2>&1 || say "  (Start Podman's socket later: systemctl --user enable --now podman.socket)"
  ca=x86_64; [ "$ARCH" = arm64 ] && ca=aarch64
  curl -fsSL -o "$TMP/docker-compose" "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$ca"
  curl -fsSL -o "$TMP/docker-compose.sha256" "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$ca.sha256"
  (cd "$TMP" && sed "s# .*# docker-compose#" docker-compose.sha256 | sha256sum -c --status) || die "Docker Compose checksum mismatch"
  run install -m 755 "$TMP/docker-compose" /usr/local/bin/docker-compose
  mh=$(getent passwd "$ME" | cut -d: -f6)
  # shellcheck disable=SC2016 # written literally: the shell expands it at login
  grep -q 'podman/podman.sock' "$mh/.profile" 2>/dev/null || printf 'export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock\n' | as_user "$ME" tee -a "$mh/.profile" >/dev/null
}

# The Whisper large-v3-turbo model, int8, in faster-whisper's format, pinned
# to a commit; checked file by file (MIT license).
MODEL_BASE=${XHAOS_MODEL_BASE:-https://huggingface.co/mukowaty/faster-whisper-int8/resolve/37ef4fc3b1b4af933c6b8db61f93d54694993699/faster-whisper-large-v3-turbo-int8}
MODEL_FILES="2caaf13921eded3ac31420512b2a253941e3fdf95abcc71c2299a4c836e63862 model.bin
b0253ea6c0d3bea6b1e19e91a02acfd3b53f4467362efcb5a3e6b16c9b3a9b7e config.json
297b13372ac43916285644fb9687add3cc62ee2a1adb60da3dc25cc94c1871fd tokenizer.json
c69260f2ab26d659b7c398f9a2b2b48ed0df16c3b47d7326782fd9cba71690c1 vocabulary.json
7ccc62c6f2765af1f3b46c00c9b5894426835a05021c8b9c01eecb6dfb542711 preprocessor_config.json"
# fetch_model downloads the files that are missing or wrong, then checks all.
fetch_model() {
  run install -d -m 755 -o xhaos -g xhaos "$DICT" "$DICT/models" "$MODEL_DIR"
  printf '%s\n' "$MODEL_FILES" | while read -r sum file; do
    if xtest -f "$MODEL_DIR/$file" && [ "$(xsum "$MODEL_DIR/$file")" = "$sum" ]; then continue; fi
    say "  downloading $file"
    as_user xhaos curl -fL --retry 3 -C - -s -o "$MODEL_DIR/$file.part" "$MODEL_BASE/$file" || die "model download failed: $file"
    [ "$(xsum "$MODEL_DIR/$file.part")" = "$sum" ] || { run rm -f "$MODEL_DIR/$file.part"; die "model checksum mismatch: $file"; }
    as_user xhaos mv "$MODEL_DIR/$file.part" "$MODEL_DIR/$file"
  done
}

install_dictation() {
  needs_apt || return 0
  apt_update; apt_install python3 python3-venv
  [ -f "$NEW/dictation/stt.py" ] || die "this release has no dictation sidecar (dictation/stt.py)"
  run install -d -m 755 -o xhaos -g xhaos "$DICT"
  say "  faster-whisper (PyPI) in a virtualenv, $DICT/venv"
  xtest -x "$DICT/venv/bin/python" || as_user xhaos python3 -m venv "$DICT/venv"
  as_user xhaos "$DICT/venv/bin/pip" install -q --disable-pip-version-check --retries 8 --timeout 60 "faster-whisper==1.2.1"
  run install -m 644 -o xhaos -g xhaos "$NEW/dictation/stt.py" "$DICT/stt.py"
  say "  Whisper large-v3-turbo (Hugging Face, 823 MB, checked file by file)"
  fetch_model
}

# The command classifier's pins: llama.cpp's prebuilt CPU build for Ubuntu
# (MIT license), tag b12110 (a nightly, commit cd959bae99ff4bed22bef2604f45241c09bbb8c8),
# one tarball per CPU, checked before it is unpacked; and the Qwen2.5-Coder
# 3B Instruct model, Q4_K_M GGUF (Apache-2.0), pinned to a commit.
LLAMA_BASE=${XHAOS_LLAMA_BASE:-https://github.com/ggml-org/llama.cpp/releases/download/$LLAMA_TAG}
LLAMA_X64_SUM=368227edcc229be5f9bed16a2223d79b5e41d0ac2e19bcf5825e82291c060e55
LLAMA_ARM64_SUM=4758b51335e37b4346251f2687d64841ff1c07ecb27fc46323945bb2ec9c44d2
CLS_MODEL_BASE=${XHAOS_CLASSIFIER_MODEL_BASE:-https://huggingface.co/Qwen/Qwen2.5-Coder-3B-Instruct-GGUF/resolve/f74adce6aa16316c625447af059dbebe4983757c}
CLS_MODEL_SUM=724fb256bec1ff062b2f65e4569e871ad2e95ab2a3989723d1769c54294730b7

# llama_ok "SUM NAME": the unpacked llama.cpp folder came from that tarball and
# every file in it still matches the list written when it was unpacked.
llama_ok() {
  xtest -x "$LLAMA_DIR/llama-server" && xtest -f "$LLAMA_SUMS" || return 1
  [ "$(run sed -n 1p "$LLAMA_SUMS")" = "# $1" ] || return 1
  # shellcheck disable=SC2016 # expanded by the inner shell
  as_user xhaos sh -c 'cd "$1" && sha256sum -c --quiet --strict --status "$2"' sh "$LLAMA_DIR" "$LLAMA_SUMS"
}

install_classifier() {
  case $ARCH in
    amd64) lname=llama-$LLAMA_TAG-bin-ubuntu-x64.tar.gz lsum=$LLAMA_X64_SUM ;;
    *) lname=llama-$LLAMA_TAG-bin-ubuntu-arm64.tar.gz lsum=$LLAMA_ARM64_SUM ;;
  esac
  run install -d -m 755 -o xhaos -g xhaos "$CLS" "$CLS/models"
  say "  llama.cpp $LLAMA_TAG (github.com/ggml-org/llama.cpp, $lname) in $LLAMA_DIR"
  if llama_ok "$lsum $lname"; then
    say "  already unpacked and checked"
  else
    tgz=$CLS/$lname.part
    if [ "$(xsum "$tgz")" != "$lsum" ]; then
      as_user xhaos curl -fL --retry 3 -C - -s -o "$tgz" "$LLAMA_BASE/$lname" || die "llama.cpp download failed: $lname"
      [ "$(xsum "$tgz")" = "$lsum" ] || { run rm -f "$tgz"; die "llama.cpp checksum mismatch: $lname"; }
    fi
    # Unpacked whole with tar, beside the old folder, then swapped in: its
    # libraries are symlinks found through RUNPATH=$ORIGIN, so it stays together.
    run rm -rf "$CLS/.unpack"
    as_user xhaos mkdir "$CLS/.unpack"
    as_user xhaos tar -xzf "$tgz" -C "$CLS/.unpack"
    new=$CLS/.unpack/llama-$LLAMA_TAG
    xtest -x "$new/llama-server" || die "the llama.cpp tarball has no llama-$LLAMA_TAG/llama-server"
    # shellcheck disable=SC2016 # expanded by the inner shell
    as_user xhaos sh -c 'cd "$1" && { printf "# %s\n" "$2"; find . -type f -exec sha256sum {} +; } >"$3"' sh "$new" "$lsum $lname" "$CLS/.unpack/sums"
    run rm -rf "$LLAMA_DIR"
    as_user xhaos mv "$new" "$LLAMA_DIR"
    as_user xhaos mv "$CLS/.unpack/sums" "$LLAMA_SUMS"
    run rm -rf "$CLS/.unpack" "$tgz"
    say "  unpacked and checked"
    touch "$TMP/classifier-changed"
  fi
  # llama-server needs libgomp and OpenSSL's libraries besides the C++ runtime.
  if ! as_user xhaos "$LLAMA_DIR/llama-server" --version >/dev/null 2>&1; then
    if [ "$APT" = yes ]; then apt_update; apt_install libgomp1 openssl; fi
    out=$(as_user xhaos "$LLAMA_DIR/llama-server" --version 2>&1) || die "llama-server does not run here: $(printf '%s' "$out" | tail -n 3)"
  fi
  say "  Qwen2.5-Coder 3B Instruct, Q4_K_M (Hugging Face, 2.1 GB, checked) in $CLS/models"
  if xtest -f "$CLS_MODEL" && [ "$(xsum "$CLS_MODEL")" = "$CLS_MODEL_SUM" ]; then
    say "  already downloaded and checked"
  else
    say "  downloading $CLS_MODEL_FILE"
    as_user xhaos curl -fL --retry 3 -C - -s -o "$CLS_MODEL.part" "$CLS_MODEL_BASE/$CLS_MODEL_FILE" || die "model download failed: $CLS_MODEL_FILE (re-run to resume)"
    [ "$(xsum "$CLS_MODEL.part")" = "$CLS_MODEL_SUM" ] || { run rm -f "$CLS_MODEL.part"; die "model checksum mismatch: $CLS_MODEL_FILE"; }
    as_user xhaos mv "$CLS_MODEL.part" "$CLS_MODEL"
    touch "$TMP/classifier-changed"
  fi
}

# classifier_remove HEADER: how to delete the classifier's files (never done
# on its own).
classifier_remove() {
  say "  $1"
  say "    sudo rm -f $CLS_UNIT"
  say "    sudo systemctl daemon-reload"
  say "    sudo rm -rf $CLS"
}

# part NAME INSTALLED DEFAULT DESCRIPTION: install or update an optional part.
# --with NAME installs or updates it without asking; --without NAME skips it;
# an installed part is otherwise left alone. A part not installed yet is
# offered on a first install, and on updates only to someone at the
# terminal: an unattended update never starts a new download on its own.
# Each part runs on its own: if it fails, the installer says so and goes on.
part() {
  case $WITHOUT in *",$1,"*) say "$1: skipped (--without)"; return 0 ;; esac
  case $WITH in
    *",$1,"*) ;;
    *)
      if [ "$2" = yes ]; then say "$1: installed (update it with --with $1)"; return 0; fi
      if [ "$FIRST" = no ] && [ "$YES" = no ] && [ -z "$TTY" ]; then
        say "$1: not installed (add it with --with $1)"; return 0
      fi
      want "$1" "$4" "$3" || return 0 ;;
  esac
  # Not in an if: there, shells ignore set -e even inside the subshell, and a
  # failed step would be skipped instead of ending the part.
  set +e
  (set -e; "install_$1")
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    eval "$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')=yes"
  else
    say "  $1 failed (above); the rest of the installation goes on. Re-run with --with $1 to try again."
  fi
}

step "Optional parts"
part devtools "$DEVTOOLS" n "Build tools and languages: gcc, make, cmake, Go, Node.js (through n), Rust (rustup).
  So agents can build and test projects. From Ubuntu's apt, go.dev, nodejs.org and rust-lang.org."
part flutter "$FLUTTER" n "Flutter and Dart (stable).
  For Flutter projects; agents get a copy their sandboxed commands can use. From github.com/flutter/flutter."
part browsers "$BROWSERS" n "Google Chrome and Firefox (not snaps), their WebDriver drivers, and Playwright's system libraries.
  For headless UI tests by agents. From Google's and Mozilla's apt repositories and GitHub (geckodriver)."
part podman "$PODMAN" n "Rootless Podman with Docker Compose.
  To run compose.yaml / docker-compose.yml stacks as your user (not for agents). From Ubuntu's apt and GitHub (checksum verified)."
part dictation "$DICTATION" y "Speech dictation: talk instead of type, transcribed on this server (nothing leaves it).
  Downloads faster-whisper (PyPI, ~470 MB) and the Whisper large-v3-turbo model (Hugging Face, 823 MB, MIT license)."
# --yes answers yes to the other parts, but never adds the classifier model
# (2.1 GB, and about 3.7 GB of RAM while it runs): only --with classifier or the terminal does.
cls_yes_only() {
  [ "$YES" = yes ] && [ "$CLASSIFIER" != yes ] || return 1
  case $WITH$WITHOUT in *",classifier,"*) return 1 ;; esac
}
if cls_yes_only; then
  say "classifier: not installed (--yes does not add it; add it with --with classifier)"
else
part classifier "$CLASSIFIER" n "Command classifier model (optional; most servers do not need it).
  Xhaos's built-in list of build, test and lint commands already runs those without asking, with no model (on by default; Settings → Tools → Commands turns it off).
  This adds a small model on this server for the long tail: commands the list does not match, for agents on auto review.
  Dangerous commands still ask, and nothing leaves the server.
  Downloads llama.cpp $LLAMA_TAG (GitHub, ~20 MB, MIT license) and Qwen2.5-Coder 3B Instruct Q4_K_M (Hugging Face, 2.1 GB, Apache-2.0),
  both checked against pinned SHA-256 sums; runs as the service xhaos-classifier on 127.0.0.1:$CLS_PORT."
fi
case $WITHOUT in *",classifier,"*)
  if [ "$CLASSIFIER" = yes ]; then
    say "  It stays installed and on: $CLS ($(run du -sh "$CLS" 2>/dev/null | cut -f1)), service xhaos-classifier."
    say "  Turn it off: Settings → Tools → Commands (server-wide), or set CLASSIFIER='no' in $CONF and re-run this installer"
    say "  (that drops -classifier-url from Xhaos's service and stops xhaos-classifier)."
    classifier_remove "Delete it: turn it off with CLASSIFIER='no' and a re-run as above, then:"
  fi ;;
esac
# An installed sidecar follows the release it came with.
if [ "$DICTATION" = yes ] && [ -f "$NEW/dictation/stt.py" ] && ! run cmp -s "$NEW/dictation/stt.py" "$DICT/stt.py"; then
  run install -m 644 -o xhaos -g xhaos "$NEW/dictation/stt.py" "$DICT/stt.py"
fi

# ---- the service ---------------------------------------------------------------

step "The service"
run install -d -m 755 "$CONF_DIR"
{
  printf '# Xhaos installer settings: edit, then re-run install.sh to apply.\n'
  printf "PROJECTS='%s'\nPUBLIC_HOST='%s'\nTRUSTED_PROXY='%s'\nLISTEN='%s'\nPORT='%s'\n" "$PROJECTS" "$PUBLIC_HOST" "$TRUSTED_PROXY" "$LISTEN" "$PORT"
  printf "DEVTOOLS='%s'\nFLUTTER='%s'\nBROWSERS='%s'\nPODMAN='%s'\nDICTATION='%s'\nCLASSIFIER='%s'\n" "$DEVTOOLS" "$FLUTTER" "$BROWSERS" "$PODMAN" "$DICTATION" "$CLASSIFIER"
} > "$TMP/install.conf"
run install -m 644 "$TMP/install.conf" "$CONF"

flags="-db $DATA/xhaos.db -sandbox on"
[ -n "$PUBLIC_HOST" ] && flags="$flags -public-host $PUBLIC_HOST"
[ -n "$TRUSTED_PROXY" ] && flags="$flags -trusted-proxy $TRUSTED_PROXY"
if [ "$DICTATION" = yes ]; then
  flags="$flags -dictation-cmd $DICT/venv/bin/python -dictation-model $MODEL_DIR -dictation-args \"$DICT/stt.py {wav} --model {model} --language={language} --prompt={prompt}\""
fi
[ "$CLASSIFIER" = yes ] && flags="$flags -classifier-url http://127.0.0.1:$CLS_PORT"
path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
[ "$FLUTTER" = yes ] && path="$path:$DATA/sandbox/flutter/bin"
{
  printf '%s\n' "$MARK"
  printf '[Unit]\nDescription=Xhaos agent workspace\nAfter=network-online.target\nWants=network-online.target\n'
  # Wanted, not required: Xhaos starts even if the classifier fails (commands then ask).
  [ "$CLASSIFIER" = yes ] && printf 'Wants=xhaos-classifier.service\nAfter=xhaos-classifier.service\n'
  printf '\n'
  printf '[Service]\nUser=xhaos\nGroup=xhaos\nWorkingDirectory=%s\nUMask=0002\n' "$HOME_DIR"
  printf 'Environment=WAILS_SERVER_HOST=%s\nEnvironment=WAILS_SERVER_PORT=%s\nEnvironment=PATH=%s\n' "$LISTEN" "$PORT" "$path"
  [ "$DEVTOOLS" = yes ] && printf 'Environment=RUSTUP_HOME=/opt/rust/rustup\n'
  printf 'ExecStart=%s %s\nRestart=on-failure\n' "$BIN" "$flags"
  printf '# Hardening that leaves the sandbox working.\nProtectSystem=strict\nReadWritePaths=%s %s\n' "$HOME_DIR" "$PROJECTS"
  printf 'PrivateTmp=yes\nProtectKernelTunables=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\n\n[Install]\nWantedBy=multi-user.target\n'
} > "$TMP/xhaos.service"

# The classifier's service, set up before Xhaos's (which wants it).
if [ "$CLASSIFIER" = yes ]; then
  cls_cmd="$LLAMA_DIR/llama-server --host 127.0.0.1 --port $CLS_PORT -m $CLS_MODEL -t 4 -c 4096"
  {
    printf '%s\n' "$MARK"
    printf '[Unit]\nDescription=Xhaos command classifier (llama.cpp server, loopback only)\n\n'
    printf '[Service]\nUser=xhaos\nGroup=xhaos\nWorkingDirectory=%s\n' "$CLS"
    printf '# It logs "no API key is set and CORS allows all origins" at start: expected,\n'
    printf '# it listens on 127.0.0.1 only, for Xhaos on this server.\n'
    printf 'ExecStart=%s\nRestart=on-failure\n' "$cls_cmd"
    printf 'ProtectSystem=strict\nReadWritePaths=%s\nPrivateTmp=yes\nNoNewPrivileges=yes\n' "$CLS"
    printf 'ProtectKernelTunables=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\n\n[Install]\nWantedBy=multi-user.target\n'
  } > "$TMP/xhaos-classifier.service"
  if [ "$SYSTEMD" = no ]; then
    run install -m 644 "$TMP/xhaos-classifier.service" "$CONF_DIR/xhaos-classifier.service.example"
    say "No systemd here: start the classifier yourself, as the xhaos user, before Xhaos:"
    say "  sudo -u xhaos $cls_cmd"
  else
    cls_changed=no
    [ -f "$TMP/classifier-changed" ] && cls_changed=yes
    if ! cmp -s "$TMP/xhaos-classifier.service" "$CLS_UNIT" 2>/dev/null; then
      run install -m 644 "$TMP/xhaos-classifier.service" "$CLS_UNIT"; run systemctl daemon-reload; cls_changed=yes
    fi
    if ! systemctl is-enabled xhaos-classifier >/dev/null 2>&1 || ! systemctl is-active xhaos-classifier >/dev/null 2>&1; then
      run systemctl enable --now xhaos-classifier; say "Classifier started (127.0.0.1:$CLS_PORT; it loads its model in a few seconds)."
    elif [ "$cls_changed" = yes ]; then
      run systemctl restart xhaos-classifier; say "Classifier restarted."
    fi
  fi
elif [ -f "$CLS_UNIT" ] || [ -n "$(run find "$CLS" -mindepth 1 ! -path "$CLS/models" 2>/dev/null | head -n 1)" ]; then
  # Off in $CONF, but here: stopped, never deleted.
  if [ "$SYSTEMD" = yes ] && [ -f "$CLS_UNIT" ] && { systemctl is-enabled xhaos-classifier >/dev/null 2>&1 || systemctl is-active xhaos-classifier >/dev/null 2>&1; }; then
    run systemctl disable --now xhaos-classifier; say "Stopped and disabled the classifier service (CLASSIFIER='no' in $CONF)."
  fi
  say "The classifier is not on, but its files are here: $CLS ($(run du -sh "$CLS" 2>/dev/null | cut -f1))."
  classifier_remove "Delete them (nothing is deleted on its own):"
fi

if [ "$SYSTEMD" = no ]; then
  run install -m 644 "$TMP/xhaos.service" "$CONF_DIR/xhaos.service.example"
  say "No systemd here (the service file it would use is $CONF_DIR/xhaos.service.example)."
  say "Start Xhaos yourself, as the xhaos user:"
  say "  sudo -u xhaos env WAILS_SERVER_HOST=$LISTEN WAILS_SERVER_PORT=$PORT $BIN $flags"
else
  UNIT_CHANGED=no
  if [ -f "$UNIT" ] && ! grep -qF "$MARK" "$UNIT"; then
    say "$UNIT was written by hand. The installer manages it from now on (settings in $CONF);"
    say "the old one is kept as $UNIT.bak. Its projects folders were kept: $PROJECTS"
    if ask "  Replace it with the managed one?" y; then run cp "$UNIT" "$UNIT.bak"; else say "  Kept; the service file is not updated."; UNIT_CHANGED=skip; fi
  fi
  if [ "$UNIT_CHANGED" != skip ] && ! cmp -s "$TMP/xhaos.service" "$UNIT" 2>/dev/null; then
    run install -m 644 "$TMP/xhaos.service" "$UNIT"; run systemctl daemon-reload; UNIT_CHANGED=yes
  fi
  if [ "$FIRST" = yes ] || ! systemctl is-enabled xhaos >/dev/null 2>&1; then
    if ask "Start Xhaos now (and at every boot)?" y; then run systemctl enable --now xhaos; say "Started."; fi
  elif [ "$CHANGED" = yes ] || [ "$UNIT_CHANGED" = yes ]; then
    if [ "$RESTART" = no ]; then say "Restart Xhaos when convenient: sudo systemctl restart xhaos"
    elif ask "Restart Xhaos now? It pauses running agents and locks every workspace until someone signs in." y; then
      run systemctl restart xhaos; say "Restarted."
    else say "Not restarted: run sudo systemctl restart xhaos when convenient."
    fi
  fi
fi

step "Done"
say "Xhaos $NEW_VERSION is installed; it listens on $LISTEN:$PORT."
if [ -z "$PUBLIC_HOST" ]; then
  say "Reach it from your computer with an SSH tunnel:  ssh -N -L $PORT:127.0.0.1:$PORT <you>@<this server>"
  say "then open http://localhost:$PORT. (With a domain, put an HTTPS reverse proxy in front: see docs/deploy.md.)"
fi
say "Settings: $CONF. Re-run this installer to update, or to add parts (--with devtools,dictation…)."
