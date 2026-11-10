#!/bin/sh
# Xhaos Pi flasher: writes Raspberry Pi OS Lite (64-bit) to an SD card and puts
# a node's first-boot files on it, so the Pi joins Xhaos on its first boot
# with no screen or keyboard (docs/nodes.md, "Easier blind setup").
#
#   curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/vX.Y.Z/flash-node.sh | sudo sh -s -- --files ~/Downloads
#
# (The Nodes tab gives this command with the server's own release in place of
# vX.Y.Z; a development server's uses main.)
#
# --files is the folder holding the user-data and network-config downloaded
# from Nodes → Add node → Raspberry Pi. The image comes from
# downloads.raspberrypi.com, is checked against its published SHA-256 and is
# kept in /var/cache/xhaos-node for the next card. Only removable or USB disks
# (and SD cards, never a built-in eMMC) that hold no mounted system folder are
# offered, and the chosen disk's name must be typed to erase it. The two files
# must be one pair (the same id) whose join code has not expired, so files
# left over from another node are never copied.
#
# Options:
#   --files DIR      folder with user-data and network-config (required)
#   --disk DEV       the card, e.g. /dev/sdb (otherwise you choose from a list)
#   --image FILE     a local Raspberry Pi OS image (.img or .img.xz) instead of the download
#   --rpiboot        write the card while it is in the Pi, through USB (rpiboot)
#   --yes            do not ask (only with --disk)
#   --list           list the disks that could be written, and stop
#   --help
#
# Environment (mostly for testing): XHAOS_PI_IMAGE_URL (the image, default the
# latest Lite arm64), XHAOS_FLASH_CACHE (default /var/cache/xhaos-node),
# XHAOS_USB_SYSFS (where USB devices are listed, default /sys/bus/usb/devices).
set -eu

IMAGE_URL=${XHAOS_PI_IMAGE_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64_latest}
CACHE=${XHAOS_FLASH_CACHE:-/var/cache/xhaos-node}
USB_SYSFS=${XHAOS_USB_SYSFS:-/sys/bus/usb/devices}
MAX_LIST_BYTES=549755813888 # 512 GiB: bigger disks are not offered in the list
FILES="" DISK="" IMAGE="" YES=no RPIBOOT=no LIST=no

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'flash-node.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<'EOF'
flash-node.sh --files DIR [options]   (as root)

  --files DIR      folder with user-data and network-config (from Nodes → Add node → Raspberry Pi)
  --disk DEV       the card, e.g. /dev/sdb (otherwise you choose from a list)
  --image FILE     a local Raspberry Pi OS image (.img or .img.xz) instead of the download
  --rpiboot        write the card while it is in the Pi, through USB (needs rpiboot)
  --yes            do not ask (only with --disk)
  --list           list the disks that could be written, and stop

Writes Raspberry Pi OS Lite (64-bit) to the card, then the two files to its
boot partition. Everything on the card is erased.
EOF
}

while [ $# -gt 0 ]; do
  case $1 in
    --files) shift; FILES=${1:-} ;;
    --files=*) FILES=${1#--files=} ;;
    --disk) shift; DISK=${1:-} ;;
    --disk=*) DISK=${1#--disk=} ;;
    --image) shift; IMAGE=${1:-} ;;
    --image=*) IMAGE=${1#--image=} ;;
    --rpiboot) RPIBOOT=yes ;;
    --yes|-y) YES=yes ;;
    --list) LIST=yes ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
  shift
done

[ "$(uname -s)" = Linux ] || die "this script runs on Linux (on Windows or macOS, use Raspberry Pi Imager and copy the two files by hand)"
has lsblk || die "needs lsblk (util-linux)"

# ---- which disks may be written ------------------------------------------------

# A disk is offered when it is removable, on USB or an SD slot, and none of
# its partitions (or anything stacked on them) is mounted as a system folder,
# used as swap, or is LVM, RAID or encrypted.
system_mount() {
  case $1 in
    /|/boot|/boot/*|/usr|/usr/*|/var|/var/*|/etc|/home|/home/*|/opt|/srv|/root|/nix|/snap|\[SWAP\]) return 0 ;;
  esac
  return 1
}

# why_not DEV: prints why DEV must not be written, nothing when it may.
why_not() {
  dev=$1
  [ -b "$dev" ] || { say "$dev is not a block device"; return; }
  type=$(lsblk -dno TYPE "$dev" 2>/dev/null | tr -d ' ')
  case $type in
    disk) ;;
    part) say "$dev is a partition: give the whole disk (e.g. /dev/sdb, not /dev/sdb1)"; return ;;
    *) say "$dev is a ${type:-unknown} device, not a disk"; return ;;
  esac
  rm=$(lsblk -dno RM "$dev" | tr -d ' ')
  tran=$(lsblk -dno TRAN "$dev" | tr -d ' ')
  case $dev in
    /dev/mmcblk*)
      # An SD card, or the computer's own eMMC (type MMC, not removable).
      mmc=$(cat "/sys/block/${dev#/dev/}/device/type" 2>/dev/null || true)
      if [ "$rm" != 1 ] && [ "$mmc" != SD ]; then
        say "$dev is a built-in ${mmc:-MMC} disk (eMMC), not an SD card"
        return
      fi ;;
    *)
      if [ "$rm" != 1 ] && [ "$tran" != usb ]; then
        say "$dev is a fixed ${tran:-internal} disk, not a removable, USB or SD card one"
        return
      fi ;;
  esac
  for t in $(lsblk -nlpo TYPE "$dev"); do
    case $t in disk|part) ;; *) say "$dev holds a $t device (LVM, RAID or encryption); close it first"; return ;; esac
  done
  lsblk -nlpo MOUNTPOINT "$dev" | while IFS= read -r m; do
    [ -n "$m" ] || continue
    if system_mount "$m"; then say "$dev is in use by this computer (mounted as $m)"; break; fi
  done
}

describe() {
  size=$(lsblk -dno SIZE "$1" | tr -d ' ')
  model=$(lsblk -dno VENDOR,MODEL "$1" 2>/dev/null | tr -s ' ' | sed 's/^ //; s/ $//')
  printf '%s  %s  %s' "$1" "$size" "${model:-(no model name)}"
}

candidates() {
  lsblk -dnpo NAME,TYPE | while read -r name type; do
    [ "$type" = disk ] || continue
    [ -z "$(why_not "$name")" ] || continue
    bytes=$(lsblk -dbno SIZE "$name" | tr -d ' ')
    [ "${bytes:-0}" -gt 0 ] || continue # an empty card reader
    [ "$bytes" -le "$MAX_LIST_BYTES" ] || continue
    say "$name"
  done
}

if [ "$LIST" = yes ]; then
  found=no
  for d in $(candidates); do describe "$d"; say ""; found=yes; done
  [ "$found" = yes ] || say "No removable or USB disk that could be written (insert the card, or see --help)."
  exit 0
fi

# ---- checks ----------------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "run as root (curl … | sudo sh -s -- …): it writes a disk"
[ -n "$FILES" ] || die "pass --files with the folder holding user-data and network-config (Nodes → Add node → Raspberry Pi)"
[ -f "$FILES/user-data" ] || die "no user-data in $FILES (a browser may have saved it as user-data.txt or user-data (1): rename it)"
[ -f "$FILES/network-config" ] || die "no network-config in $FILES (a browser may have saved it as network-config.txt: rename it)"
[ "$(head -n 1 "$FILES/user-data")" = "#cloud-config" ] || die "$FILES/user-data is not a cloud-init file (its first line should be #cloud-config)"

# The pair: both files carry the same "# xhaos-node-files id=… expires=…"
# line. A browser that finds an older file saves the new one as
# "user-data (1)" or "user-data.txt": then the old one would be copied.
stamp() { grep -m 1 '^# xhaos-node-files id=' "$1" 2>/dev/null || true; }
field() { printf '%s\n' "$1" | tr ' ' '\n' | sed -n "s/^$2=//p"; }
for f in user-data network-config; do
  for other in "$FILES/$f"?* "$FILES/$f".*; do
    [ -f "$other" ] || continue
    [ -n "$(find "$other" -prune -newer "$FILES/$f")" ] || continue
    # The same pair downloaded twice is fine; anything else is not.
    if [ "$(field "$(stamp "$other")" id)" != "$(field "$(stamp "$FILES/$f")" id)" ]; then
      die "$other is newer than $FILES/$f: the browser kept an older $f. Delete the old files, rename the new ones to user-data and network-config, and run again."
    fi
  done
done
user_stamp=$(stamp "$FILES/user-data")
net_stamp=$(stamp "$FILES/network-config")
[ -n "$user_stamp" ] || die "$FILES/user-data has no xhaos-node-files line: make new files on the Nodes tab (Pi files)"
[ -n "$net_stamp" ] || die "$FILES/network-config has no xhaos-node-files line: it is not from Xhaos, or from an older Xhaos; make new files on the Nodes tab"
[ "$(field "$user_stamp" id)" = "$(field "$net_stamp" id)" ] || die "user-data and network-config in $FILES were made for different nodes or at different times (their ids differ): delete both and download them again"
expires=$(field "$user_stamp" expires)
case $expires in ''|*[!0-9]*) die "$FILES/user-data has a broken xhaos-node-files line" ;; esac
[ "$expires" -gt "$(date +%s)" ] || die "the join code in $FILES/user-data has expired (codes last an hour): on the Nodes tab, make new Pi files"
say "First-boot files for node $(field "$user_stamp" node); the join code expires in $(( (expires - $(date +%s)) / 60 )) minutes."
if [ "$YES" = yes ] && [ -z "$DISK" ]; then die "--yes needs --disk: the disk to erase must be named"; fi
for tool in curl sha256sum xz dd mount umount; do has $tool || die "needs $tool"; done
if [ -z "$DISK" ] && [ "$YES" = no ] && ! (: </dev/tty) 2>/dev/null; then
  die "no terminal to choose the disk on: pass --disk /dev/sdX --yes"
fi

WORK=$(mktemp -d)
MNT=""
cleanup() {
  if [ -n "$MNT" ]; then umount "$MNT" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---- the image ---------------------------------------------------------------------

RETRY="--retry 4 --retry-delay 3"
curl --help all 2>/dev/null | grep -q -- --retry-all-errors && RETRY="$RETRY --retry-all-errors"
# shellcheck disable=SC2086
dl() { curl $RETRY "$@"; }

step "Raspberry Pi OS Lite (64-bit)"
if [ -n "$IMAGE" ]; then
  [ -f "$IMAGE" ] || die "no such image: $IMAGE"
  say "Using $IMAGE (not checked: it is your file)."
else
  url=$(dl -fsSLI -o /dev/null -w '%{url_effective}' "$IMAGE_URL") || die "could not reach $IMAGE_URL"
  case $url in https://*.img.xz) ;; *) die "unexpected image address: $url" ;; esac
  IMAGE=$CACHE/${url##*/}
  mkdir -p "$CACHE"
  dl -fsSL -o "$WORK/image.sha256" "$url.sha256" || die "download failed: ${url##*/}.sha256"
  want=$(cut -d ' ' -f 1 "$WORK/image.sha256")
  [ ${#want} -eq 64 ] || die "${url##*/}.sha256 does not hold a SHA-256"
  if [ -f "$IMAGE" ] && [ "$(sha256sum "$IMAGE" | cut -d ' ' -f 1)" = "$want" ]; then
    say "Using the copy in $CACHE ($(basename "$IMAGE"))."
  else
    say "Downloading $(basename "$IMAGE") (about 500 MB)…"
    rm -f "$IMAGE.part"
    dl -fL --progress-bar -o "$IMAGE.part" "$url" || die "download failed: $url"
    [ "$(sha256sum "$IMAGE.part" | cut -d ' ' -f 1)" = "$want" ] || { rm -f "$IMAGE.part"; die "checksum mismatch: the download does not match ${url##*/}.sha256"; }
    mv -f "$IMAGE.part" "$IMAGE"
    # Older images in the cache are not needed again.
    for old in "$CACHE"/*.img.xz; do [ "$old" = "$IMAGE" ] || rm -f "$old"; done
  fi
  say "Checksum OK."
fi
case $IMAGE in
  *.img.xz) xz -t "$IMAGE" 2>/dev/null || die "$IMAGE is not a valid .xz file" ;;
  *.img) ;;
  *) die "the image should be a .img or .img.xz file: $IMAGE" ;;
esac

# ---- rpiboot: the Pi's own card as a USB disk -----------------------------------------

if [ "$RPIBOOT" = yes ]; then
  step "rpiboot"
  has rpiboot || die "needs rpiboot (Debian, Ubuntu, Raspberry Pi OS: apt install rpiboot)"
  lsblk -dnpo NAME | sort >"$WORK/before"
  say "Connect the Pi to this computer by USB now:"
  say "  Pi Zero 2 W: a data cable in the port marked USB (not PWR). Its card must not be bootable (blank, or no card yet), or it boots from the card instead."
  say "  Pi 5: unplug its power, hold the power button, and connect USB-C from this computer. This works even when the card already boots."
  say "Waiting for the Pi (Ctrl+C to stop)…"
  # The boot ROM shows up as 0a5c:<chip>: 2764 is the Zero 2 W's BCM2710,
  # 2711 a Pi 4, 2712 a Pi 5 (2763: older Pis, legacy firmware only).
  CHIP="" waited=0
  while [ -z "$CHIP" ]; do
    for dev in "$USB_SYSFS"/*; do
      [ "$(cat "$dev/idVendor" 2>/dev/null)" = 0a5c ] || continue
      case $(cat "$dev/idProduct" 2>/dev/null) in 2763|2764|2711|2712) CHIP=$(cat "$dev/idProduct"); break ;; esac
    done
    [ -n "$CHIP" ] && break
    sleep 1; waited=$((waited + 1))
    if [ "$waited" -eq 20 ]; then
      say "Nothing yet. Usual causes: a charge-only cable (try another), the Zero's PWR port instead of USB,"
      say "  or a card that boots (the Zero then starts from it instead: use a blank card; without a card it shows up too, but with no disk)."
      say "  A Pi 5 must have no power at all before you hold the button. Still waiting…"
    fi
  done
  GADGET=""
  for d in /usr/share/rpiboot/mass-storage-gadget64 /usr/local/share/rpiboot/mass-storage-gadget64; do
    [ -d "$d" ] && { GADGET=$d; break; }
  done
  case $CHIP in
    2711|2712)
      [ -n "$GADGET" ] || die "rpiboot's mass-storage-gadget64 is missing (apt install rpiboot installs it in /usr/share/rpiboot)"
      say "Found a Raspberry Pi (BCM$CHIP); sending it the USB disk system…"
      rpiboot -d "$GADGET" || die "rpiboot failed (see above)" ;;
    *)
      # mass-storage-gadget64 knows the BCM2710 (Zero 2 W) only since usbboot
      # of 2025-08-21; older rpiboot packages (Ubuntu up to 26.04) do not, so
      # they get the legacy msd firmware built into rpiboot: slower, but in
      # every version.
      ver=$(dpkg-query -W -f='${Version}' rpiboot 2>/dev/null | grep -o '20[0-9]\{6\}' | head -n 1 || true)
      if [ "$CHIP" = 2764 ] && [ -n "$GADGET" ] && [ "${ver:-0}" -ge 20250821 ]; then
        say "Found a Pi Zero 2 W (BCM2710); sending it the USB disk system…"
        rpiboot -d "$GADGET" || die "rpiboot failed (see above)"
      else
        say "Found a Raspberry Pi (USB id 0a5c:$CHIP); using rpiboot's built-in disk firmware (legacy msd)."
        if [ "$CHIP" = 2764 ] && [ -z "$GADGET" ]; then
          say "  Writing is slower with it; mass-storage-gadget64 (faster) is not installed."
        elif [ "$CHIP" = 2764 ]; then
          say "  Writing is slower with it; an rpiboot from 2025-08-21 or later (rpiboot ${ver:-not from apt} here) would use the faster mass-storage-gadget64."
        fi
        rpiboot || die "rpiboot failed (see above)"
      fi ;;
  esac
  say "The Pi is starting its USB disk mode; waiting for its card to appear…"
  i=0
  while [ $i -lt 60 ]; do
    for d in $(lsblk -dnpo NAME | sort | comm -13 "$WORK/before" -); do
      bytes=$(lsblk -dbno SIZE "$d" 2>/dev/null | tr -d ' ')
      if [ "${bytes:-0}" -gt 0 ]; then DISK=$d; break; fi
    done
    [ -z "$DISK" ] || break
    sleep 1; i=$((i + 1))
  done
  [ -n "$DISK" ] || die "no new disk appeared after rpiboot; is there a card in the Pi?"
  say "The Pi's card is $DISK."
fi

# ---- choose and confirm the disk -------------------------------------------------------

step "The card"
if [ -z "$DISK" ]; then
  # Device names have no spaces.
  # shellcheck disable=SC2046
  set -- $(candidates)
  [ $# -gt 0 ] || die "no removable or USB disk that could be written: insert the card (or use --disk)"
  i=1
  for d in "$@"; do printf '  %d) %s\n' "$i" "$(describe "$d")"; i=$((i + 1)); done
  if [ $# -eq 1 ]; then DISK=$1; else
    printf 'Which one (1-%d)? ' $# >/dev/tty
    read -r n </dev/tty || die "no answer"
    case $n in ''|*[!0-9]*) die "not a number: $n" ;; esac
    if [ "$n" -lt 1 ] || [ "$n" -gt $# ]; then die "no disk number $n"; fi
    eval "DISK=\${$n}"
  fi
fi
reason=$(why_not "$DISK")
[ -z "$reason" ] || die "will not write $DISK: $reason"
say "$(describe "$DISK")"
if [ "$YES" = no ]; then
  short=${DISK#/dev/}
  printf 'Everything on %s will be erased. Type %s to go on: ' "$DISK" "$short" >/dev/tty
  read -r answer </dev/tty || die "no answer"
  [ "$answer" = "$short" ] || die "not confirmed; nothing was written"
fi

# ---- write ---------------------------------------------------------------------------------

step "Writing $DISK"
for part in $(lsblk -nlpo NAME "$DISK"); do
  m=$(lsblk -no MOUNTPOINT "$part" 2>/dev/null | head -n 1)
  [ -z "$m" ] || umount "$part" || die "could not unmount $part ($m); close what uses it"
done
case $IMAGE in
  *.xz) xz -dc "$IMAGE" | dd of="$DISK" bs=4M conv=fsync status=progress 2>&1 ;;
  *) dd if="$IMAGE" of="$DISK" bs=4M conv=fsync status=progress 2>&1 ;;
esac || die "writing $DISK failed"
sync
say "Written."

# ---- the first-boot files ---------------------------------------------------------------

step "First-boot files"
if has partprobe; then partprobe "$DISK" 2>/dev/null || true; else blockdev --rereadpt "$DISK" 2>/dev/null || true; fi
if has udevadm; then udevadm settle 2>/dev/null || true; fi
case $DISK in *[0-9]) BOOTPART=${DISK}p1 ;; *) BOOTPART=${DISK}1 ;; esac
i=0
while [ ! -b "$BOOTPART" ] && [ $i -lt 20 ]; do sleep 0.5; i=$((i + 1)); done
[ -b "$BOOTPART" ] || die "the card's boot partition $BOOTPART did not appear; copy user-data and network-config to it by hand"
# A desktop may automount the new partition: unmount it first.
m=$(lsblk -no MOUNTPOINT "$BOOTPART" | head -n 1)
[ -z "$m" ] || umount "$BOOTPART" 2>/dev/null || true
MNT=$WORK/boot
mkdir -p "$MNT"
mount -t vfat "$BOOTPART" "$MNT" || die "could not mount $BOOTPART"
[ -f "$MNT/config.txt" ] || die "$BOOTPART does not look like a Raspberry Pi boot partition (no config.txt)"
for f in user-data network-config; do
  cp "$FILES/$f" "$MNT/$f"
  cmp -s "$FILES/$f" "$MNT/$f" || die "$f did not copy correctly"
done
[ -f "$MNT/meta-data" ] || : >"$MNT/meta-data"
sync
umount "$MNT"
MNT=""
if has eject; then eject "$DISK" 2>/dev/null || true; fi

step "Done"
say "Put the card in the Pi and power it. It joins on its first boot (a Pi Zero 2 W takes a few minutes);"
say "its card on the Nodes tab turns online. The join code works once and expires an hour after it was made."
say "Until the node has joined, the card's boot partition holds the Wi-Fi password and the join code in plain text;"
say "once it is connected, the Pi deletes both files (a deleted file may still be recoverable from the card)."
