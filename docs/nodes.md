# Nodes (design)

Status: phase 1 is built and tested locally, not yet on real Pis or dev-rw-07. Later phases are proposals; [Phases](#phases) lists them.

Xhaos runs agents on the machine it is installed on. Real work often needs other machines: a strong PC with GPUs to compile on, a Raspberry Pi 5 and a Pi Zero 2 W to test the binary on, with their cameras plugged in. Those machines sit on private networks, behind NAT, often without a screen. A **node** is such a machine, joined to an Xhaos workspace, that agents can run commands on and copy files to and from, under the same approvals as everything else.

## Words

- **Node**: a machine joined to one workspace. It has a name (`build-box`, `pi5`, `pizero`), facts it reports (OS, architecture, kernel, CPU, memory, sandbox support), and labels you give it (`gpu`, `camera`).
- **`xhaos-node`**: the small program that runs on a node. It is a separate binary, installed on its own. Like git's subcommands, `xhaos node <args>` on a machine that has both runs `xhaos-node <args>` (found beside `xhaos` or on `PATH`); a Pi only needs `xhaos-node`.
- **Join code**: a one-time code the owner makes in Xhaos to join one machine.
- **Node root**: the folder on the node that Xhaos may use (default `~/xhaos-work`). Everything an agent does on the node happens inside it.
- **Job**: a command running on a node in the background (a build, a 10-minute camera test), with its output kept on the node until it is read.

## How a node connects

The node dials out; Xhaos never connects in.

```
pi5 ──────┐
pizero ───┼── wss://xhaos.example.com/xhaos/node ──▶ xhaos (public address)
build-box ┘   one WebSocket per node, opened by the node    ├─ app/node: nodes, join codes, sessions
                                                           └─ tools node_* ──▶ the node's session
```

- The Xhaos server usually has a public address; the machines usually do not. A connection from the node out to the server works behind NAT, on any Wi-Fi, with no open port on the node, no VPN (Tailscale is not needed for nodes), and no SSH key stored in Xhaos.
- One long-lived WebSocket per node, over the same HTTPS as the page (so the reverse proxy and its certificate already cover it). Requests from the server and replies from the node are multiplexed on it by request ID. The node reconnects with backoff (1 s up to 60 s) and keeps its jobs running while it is away.
- The endpoint `/xhaos/node` does not use browser sessions. It is handled before the session check, like published channel links: the node proves who it is with its own key (below). A node of a locked workspace stays connected but gets no work, as that workspace's agents are paused anyway.

### A public address for nodes only

Nodes need to reach the server at a public HTTPS address. Xhaos's page and its API should not get one: keep reaching them as today (an SSH tunnel to `127.0.0.1:5180`, or a private network). Put a reverse proxy in front with a domain and a certificate, and let it pass **only** `/xhaos/node` (and, if you want them, published channel links, `/xhaos/channel/`):

```caddyfile
nodes.example.com {
	@node path /xhaos/node /xhaos/channel/*
	handle @node {
		reverse_proxy 127.0.0.1:5180
	}
	handle {
		respond 404
	}
}
```

Xhaos learns this address from `-node-url https://nodes.example.com` (with `-public-host nodes.example.com`, so the WebSocket's `Host` is accepted). The join command and the Raspberry Pi files carry that URL; **Add node** is disabled, with the reason, until it is set. The page itself stays unreachable from that domain.

## Joining

1. **Nodes** tab → **Add node**: name (default `Node 1`, `Node 2`…), and labels. Xhaos makes a join code (random, single use, expires after 15 minutes whether used or not; only its hash is stored, and it is deleted when used or expired) and shows it once:
   ```sh
   curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/main/install-node.sh | sudo sh -s -- --server https://nodes.example.com --code XXXX-XXXX-XXXX
   ```
2. `install-node.sh` downloads `xhaos-node` for the machine's architecture from the GitHub release and checks it against the release's `SHA256SUMS`, as `install.sh` does. It then creates the `xhaos-node` system user (no password, no sudo) and its root (`/var/lib/xhaos-node/xhaos-work` unless `--root` says otherwise), runs `xhaos-node join` as that user, and installs a systemd service. A release server pins the node to its own release (`--version`). Run it again without `--code` to update.
3. `xhaos-node join` makes an Ed25519 key pair (the private key stays in `/var/lib/xhaos-node/`, readable only by its user), sends the public key, the code and its facts. The server checks the code, burns it, and stores the node with its public key: the public key is the only credential kept on the server. The card turns online.
4. Each later connection answers a fresh challenge from the server by signing it, and the server only talks to a node whose key it stored. The node keeps the server's URL; it only ever connects to that server, over HTTPS (it refuses plain HTTP except to `localhost`, for development).

Removing a node in Xhaos forgets its key: its next connection is refused. `xhaos-node leave` on the machine deletes its key and stops the service.

## Raspberry Pi first boot (no screen, no keyboard)

Raspberry Pi OS based on Debian Trixie (images from November 2025 on) configures itself on first boot with cloud-init, from two files on the card's boot partition: `user-data` and `network-config`. That partition is the FAT one any computer mounts when the card is inserted, so it is the "flash disk with a setup file" we want, with nothing extra to plug in.

**Add node** → **Raspberry Pi first boot** asks for the hostname, the Wi-Fi network, its password and country code, and optionally an SSH public key for yourself. It downloads `user-data` and `network-config`:

- `user-data` creates a user (password login locked; your SSH key if given), sets the hostname, and in `runcmd` runs `install-node.sh` with the server's URL and the join code.
- `network-config` joins the Wi-Fi.

Then: flash Raspberry Pi OS Lite (64-bit) with Raspberry Pi Imager or `dd`, copy the two files onto the boot partition (replacing Imager's), put the card in the Pi and power it. A minute or two later the node appears online. The Wi-Fi password is written into the downloaded file only; Xhaos does not store it.

The files hold the Wi-Fi password and a join code, in plain text, on a partition any computer can read, and they stay there after first boot. The download says so next to the button. The code is single use and expires after 1 hour, used or not (other join codes keep 15 minutes; flashing a card and a Pi Zero 2 W's first boot take longer); after the Pi has joined, the code on the card is worthless, but the Wi-Fi password is not. The plan is for the first boot to delete both files from the boot partition once the node has joined (see Open questions). Older images (Bookworm, `firstrun.sh`) are not supported: use a Trixie image. A Pi Zero 2 W runs the 64-bit image, so `linux/arm64` covers both Pis.

## Easier blind setup (phase 2)

The two-file path above needs a computer that can flash a card and copy files onto it. Three additions, in the order they would be built. None of them changes the protocol: each ends with the same `install-node.sh` join.

What the Raspberry Pi documentation says (read for this design, not tried on our Pis):

- The Zero 2 W boots from a USB mass-storage device (a USB stick) out of the box. The Pi 5 does too. Models older than the Pi 4 "have known issues which prevent booting with some USB devices", so some sticks will not work on the Zero 2 W.
- Both try the SD card first. A stick boots only when the SD card is missing, blank or has nothing bootable on it. A card that already has Raspberry Pi OS on it boots instead.
- On the Zero 2 W the stick goes in the data port (marked USB, not PWR) through an OTG adapter.
- The Zero 2 W and the Pi 5 also support `rpiboot` with `mass-storage-gadget` (raspberrypi/usbboot). The computer sends the Pi a small Linux system over USB, and the Pi shows its own SD card to that computer as a USB disk. The card can then be written without taking it out. A Zero 2 W only does this when its card does not boot (it has no rpiboot button). A Pi 5 does it even when its card boots: unplug its power, hold the power button, and connect USB-C from the computer. So on a Pi 5, an existing card can be overwritten without taking it out.

### 1. One-command flasher: `flash-node.sh` (built)

A script for a Linux computer that does the flashing and the copying in one go. Add node → Raspberry Pi shows the command after the two downloads:

```
curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/main/flash-node.sh | sudo sh -s -- --files ~/Downloads
```

- It lists whole disks that are removable, on USB or in an SD slot (`lsblk` `RM=1`, `TRAN=usb`, `mmcblk*`), up to 512 GiB. Disks with a partition mounted as a system folder (`/`, `/boot`, `/usr`, `/var`, `/home`…) or used as swap, LVM, RAID or encryption are never listed or written. Other mounted partitions (a desktop's automount) are unmounted first. It asks you to type the chosen disk's name (`sdb`) to confirm. `--disk /dev/sdX` skips the menu but not these checks; `--yes` skips typing and needs `--disk`. `--list` shows what could be written, without root.
- It downloads Raspberry Pi OS Lite arm64 (latest, from downloads.raspberrypi.com), checks it against the published SHA-256, and caches it in `/var/cache/xhaos-node/` (the script runs as root), keeping only the newest image. `--image FILE` uses a local image.
- It writes the card, mounts its boot partition, checks it holds `config.txt`, copies `user-data` and `network-config` from `--files` (and compares them), adds an empty `meta-data` if the image has none, syncs and ejects. It refuses a `user-data` whose first line is not `#cloud-config`, and says so when a browser saved the files under another name (`user-data (1)`, `.txt`).
- It takes the two downloaded files rather than the Wi-Fi password or code as options, so no secret ends up in shell history or `ps`.
- `--rpiboot` first runs `rpiboot -d mass-storage-gadget64` so a Pi plugged in by USB shows its card as a disk, then continues the same way. This needs `rpiboot` installed (Debian/Ubuntu package `rpiboot`). On a Zero 2 W it only works when the card in the Pi does not boot; on a Pi 5 it also overwrites a card that boots (see above). The first new disk that appears after `rpiboot` is offered, and its name must still be typed.

### 2. USB stick config on a running node (built)

This lets a node that is already set up get new settings without SSH or a screen: new Wi-Fi, or joining again after it was removed. A stick with no settings on it is also a way to find a node: plugging it in writes the node's addresses to it.

Format a stick as FAT32 or exFAT, labelled `XHAOS`, and put `xhaos-node.conf` at its top level:

```
# lines starting with # are comments
wifi_ssid=Lab 5G
wifi_password=correct horse battery
wifi_country=RW
# to join (again): a fresh code from Nodes → Add node or New code
server=https://xhaos.example.com
code=ABCD-EFGH-JKMN
```

- Every line is optional. The known settings are `wifi_ssid`, `wifi_password` (leave it out for an open network), `wifi_country`, `wifi_hidden` (`yes` for a hidden network), `server` and `code`. Without `server=`, `code=` joins the server the node already knows. Spaces around a value are dropped; put it in double quotes to keep them.
- Any mistake (an unknown setting, a setting given twice, a password shorter than 8 characters) rejects the whole file, so a stick is applied completely or not at all.
- `install-node.sh` (with systemd) adds a udev rule, `/etc/udev/rules.d/90-xhaos-node-usb.rules`. When a FAT or exFAT file system labelled `XHAOS` appears, the rule starts `xhaos-node-usb@<partition>.service`. This is a one-shot service that runs `xhaos-node apply-usb /dev/<partition>` as root. The service also runs at boot when the stick is already plugged in. Only this service runs as root; the node service stays unprivileged.
- `apply-usb` mounts the stick on its own with `nosuid,nodev,noexec`, so nothing on it can run.
- **Wi-Fi:** it writes a NetworkManager connection, `xhaos-usb-<ssid>`, to `/etc/NetworkManager/system-connections/` (readable by root only). The connection is preferred over the node's other networks. It then reloads NetworkManager and connects (waiting up to 45 s). A network out of reach is kept for later, and the node stays on its other networks meanwhile. `wifi_country` uses `raspi-config` where there is one, otherwise `iw reg set`, which lasts until the next restart.
- **Joining:** it joins with the code into a temporary folder, retrying for up to 2 minutes while the network comes up. It stops at once when the server refuses the code. Only after the join succeeds does it stop the node service, replace the key and settings (owned by the `xhaos-node` user), and start the service again. A failed join leaves the old key. The node keeps its folder, devices and sandbox mode. The node's old entry, on its old server, stays there as offline until it is removed.
- **Result:** it writes `xhaos-node-result.txt` back to the stick: the time, what it did, any errors, and the hostname, IP addresses, active connections, the node it is joined as and whether its service runs. Plug the stick in, wait a minute, unplug it and read the file on a computer. There is no LED signal yet.
- **Secrets:** whatever happened, it rewrites `xhaos-node.conf` with every setting commented out and the password and the code removed. Plugging the stick in again only writes a new result. The password then lives only in NetworkManager's root-only file.
- `install-node.sh --no-usb-config` leaves the rule out, or removes it. Anyone who can plug in a stick can move the node to their own server. For a Pi that adds little, since anyone holding it can already take its card. For a build PC in a shared room, use this option.

### 3. USB installer stick (writes the SD card)

This is for setting up a Pi from nothing without a second computer to flash cards.

- One installer image for both Pis: a small Linux system (the Raspberry Pi OS arm64 kernel, which supports both the Zero 2 W and the Pi 5) and the Raspberry Pi OS Lite image. `xhaos.txt` goes on its FAT partition, with the same settings as `xhaos-node.conf`. Add node can download a ready `xhaos.txt`.
- Boot the Pi with a blank card in it and the stick plugged in. The installer writes Raspberry Pi OS to the card and writes `user-data` and `network-config` from `xhaos.txt`. It writes a result file to the stick, removes the password from it, and powers off. Unplug the stick and power on again: the Pi boots from the card and joins.
- Feedback without a screen: the activity LED (steady while writing, slow blink when done, fast blink on error) and the result file.
- Cost: the image has to be built (pi-gen, or a buildroot initramfs like usbboot's `mass-storage-gadget`) and published with each release. It is about the size of Raspberry Pi OS Lite (about 500 MB compressed).
- Limit: because the SD card boots first, the stick cannot replace a card that already boots. Making such a card unbootable from the running system, so the next boot falls through to the stick, is possible in principle but is left out until it has been tried on both Pis.

## On the node

`xhaos-node` is small: no page, no database, no model code. Pure Go, static, for `linux/amd64` and `linux/arm64` (and `linux/arm` for 32-bit systems if needed). It runs as its own unprivileged user under systemd.

The node is the last line of defence, so it enforces its own limits, whatever the server asks:

- Commands and file operations stay inside the node root. Commands run under Landlock (the same sandbox package as the server: read and run system files, write only the root and a temp folder). **Without Landlock the node refuses to run commands.** Only an owner on the machine itself can lift that for that node, with `xhaos-node join --unsandboxed` (or `install-node.sh --unsandboxed`); the server cannot ask for it, and the node's card shows "not sandboxed" in red. The node reports its Landlock version when it joins.
- Devices are opt-in on the node: `--device /dev/video0 --device /dev/ttyUSB0` (or the user's group membership, `video`, `dialout`, when not sandboxed). Without them a sandboxed command cannot open hardware.
- No `sudo`: the `xhaos-node` user has none. Installing packages on a node is the owner's job.
- Commands have a time limit; jobs have an output cap (the newest 10 MB are kept).

### Trust

**Whoever controls the Xhaos server can run commands on every node joined to it.** That includes anyone who signs in to the node's workspace, and anyone who breaks into the server. The approvals in the page bound what agents do; they do not bound the server. What bounds the damage is on the node: its root folder, Landlock, the devices it opted into, and its unprivileged user. Join only machines you would give that server a shell on, put nothing on them you could not lose, and keep nodes in the workspace that needs them. Removing a node in Xhaos, or `xhaos-node leave` on the machine, ends it.

## For agents

A node reaches an agent only after the owner grants it to that agent (like secrets); none is granted by default, and subagents get only what their parent has and passes on. Tools:

| Tool | Does | Effect |
|---|---|---|
| `nodes_list` | Nodes granted to the agent: name, online, OS/arch, kernel, CPU, memory, labels, sandbox, root, running jobs. | read |
| `node_run` | Run a command on a node, in its root (or a folder inside it). Waits up to 2 minutes; `background: true` starts a job and returns its ID at once. | read for read-only commands (same classifier as `bash`), else exec |
| `node_job` | A job's new output (`logs`), wait for it (`wait`, up to 2 minutes), or stop it (`kill`). | read; `kill` is exec |
| `node_copy` | Copy a file or folder: project → node, node → project, or node → node. Relayed through the server and streamed in chunks (never held whole in memory), with a size limit per copy (1 GiB by default); the receiver checks the SHA-256 at the end and discards the file if it does not match. From a project, a folder copy skips `.git` and git-ignored files. | write |

Approvals work as for `bash`: under manual review a node command waits for Allow, and the card shows the node's name with the command. Read-only agents may list and read, not run or copy onto a node. Dangerously allow covers node calls like any other. Output comes back with the same 30,000-byte cap as `bash`.

A typical run: `node_copy` the project to `build-box:app/`, `node_run build-box "cd app && make -j32" background`, poll with `node_job`, `node_copy build-box:app/build/recorder pi5:recorder`, `node_run pi5 "./recorder --seconds 60"`, read the output.

## In the page

- **Nodes** is a tab of the side panel: one card per node with its name, a state dot (online, offline with last seen, waiting to join with the code's expiry), what it is (model or OS/arch, distro, CPUs, memory, `xhaos-node` version), its folder and sandbox state, devices, labels, the agents it is granted to (one chip per agent; pressed means granted), and its jobs (running ones with a Stop). Card actions: rename and relabel inline, remove (with a confirm); a waiting node also has **New code** and **Pi files**. **Add node** (the panel's heading) asks for a name and labels, with a switch for the Raspberry Pi first-boot files; the card appears, waiting, with the command or the files shown once on it, and turns online when the machine joins.
- Tool calls on a node are summarised as `node: command` in the transcript and on approval cards.

## Protocol

JSON text frames, one message per frame; file data in binary frames tagged with the request ID. Every request has an `id`; the node answers with one or more messages carrying it.

| From | Message | Fields |
|---|---|---|
| node | `hello` | node ID, version, facts |
| server | `challenge` / node `proof` | nonce / signature |
| server | `exec` | command, dir, timeout, background |
| node | `out` / `exit` | stream, data / code, duration (or job ID for background) |
| server | `job` | job ID, action (logs from offset, wait, kill) |
| server | `put` / `get` | path, mode, size, SHA-256 / path |
| server | `cancel` | request ID |
| both | `ping` / `pong` | |

The version is checked on `hello`; a node too old for the server is told so and gets no work.

## Code layout

- `cmd/xhaos-node`: the node binary (`join`, `run`, `leave`, `version`). Imports `app/node/wire` and `app/sandbox`, not Wails.
- `app/node`: nodes, join codes and grants (tables), the WebSocket endpoint, live sessions, and the `node_*` tools (an `ExternalTools` source for `run`).
- `app/node/wire`: the message types, shared by both sides.
- `install-node.sh` and `flash-node.sh`, and `xhaos-node` for `linux/amd64` and `linux/arm64` in the same release workflow, listed in the same `SHA256SUMS`.

## Phases

1. **Run and copy, and join a Pi without a screen.** `xhaos-node` (join, run as a service, exec, jobs, put/get, sandbox, devices); `app/node` with join codes, keys, sessions and grants; the four tools; the Nodes tab (add with a join command or the Raspberry Pi first-boot files, list, grant, remove, jobs); `install-node.sh`; release builds. Enough to build on a strong machine and test on Pis.
2. **Easier blind setup**, in this order: `flash-node.sh` (with `--rpiboot`) and the USB stick config (both built), then the USB installer stick (see [Easier blind setup](#easier-blind-setup-phase-2)).
3. **Hardware.** Hardware facts (cameras, GPUs, USB devices) on node cards; picking a node by label ("any `pi5`").
4. **Projects on nodes.** A project can live on a node (node + folder), so an agent's ordinary tools (`read_file`, `edit_file`, `bash`…) run there. Incremental sync of a project to a node. Nodes update themselves when the server is updated.

## Open questions

- Does Raspberry Pi OS's kernel enable Landlock? If not, the Pis need `--unsandboxed` (or a kernel with it) before they run anything. Check on a real Pi first.
- Can the first boot delete `user-data` and `network-config` from the boot partition without cloud-init treating the next boot as a new machine and redoing the network? Check on a real Pi; if it cannot, the download warning is the only guard.
- USB boot on our hardware: which of our sticks the Zero 2 W boots from, and whether the Pi 5's bootloader on our units has USB in `BOOT_ORDER` (it is in the default). Check this before building the installer stick.
- Should a node be shareable between workspaces? This design says no: one node, one workspace.
