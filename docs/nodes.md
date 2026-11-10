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

#### `scripts/expose-nodes.sh` (Debian/Ubuntu, after `install.sh`)

Point the domain's A record (and AAAA, if it has one) at the server, then run once:

```sh
curl -fsSL -o expose-nodes.sh https://raw.githubusercontent.com/devark28/xhaos/main/scripts/expose-nodes.sh
sudo sh expose-nodes.sh nodes.example.com --dry-run   # see what it would change
sudo sh expose-nodes.sh nodes.example.com
```

It is separate from `install.sh` because it changes the whole machine. Before changing anything it checks that the domain resolves to this server (and says to switch a Cloudflare-proxied record to "DNS only"), and refuses when anything but Caddy listens on ports 80 or 443. It then shows its plan and asks (`--yes` skips the questions):

1. With ufw active, `ufw allow 80,443/tcp` (an inactive ufw is left off). A provider's firewall is yours to open.
2. `apt-get install caddy`, and a site like the one above but for `/xhaos/node` only (everything else 404) in its own file, `/etc/caddy/sites/xhaos-nodes.caddy`, with one `import /etc/caddy/sites/*.caddy` line in `/etc/caddy/Caddyfile`. A Caddyfile with sites of its own keeps them; only the untouched stock one (its `:80` placeholder site answers every host on port 80) is replaced, kept as `Caddyfile.stock`. `caddy validate` must pass, or the files are put back; then `systemctl reload caddy`.
3. `PUBLIC_HOST='nodes.example.com'` in `/etc/xhaos/install.conf`, applied by re-running `install.sh --version <installed release> --no-restart` (`NODE_URL` and `TRUSTED_PROXY` follow from it). It asks before restarting Xhaos, which pauses running agents.
4. Checks `https://nodes.example.com/xhaos/node` answers 426 (a WebSocket is expected) and `/` answers 404. A 404 on the node path means the Caddy site is wrong, 403 that Xhaos does not have `-public-host` yet (not restarted), 502 that Xhaos is not running.

Re-running it with the same domain changes nothing. `--port N` sets the port Caddy passes to (default `PORT` from `install.conf`, 5180).

By hand, the same: open 80/443, install Caddy, add the site above, set `PUBLIC_HOST` in `/etc/xhaos/install.conf`, re-run `install.sh`, restart Xhaos, and check with `curl -sI https://nodes.example.com/xhaos/node` (426).

## Joining

1. **Nodes** tab → **Add node**: name (default `Node 1`, `Node 2`…), and labels. Xhaos makes a join code (random, single use, expires after 15 minutes whether used or not; only its hash is stored, and it is deleted when used or expired) and shows it once:
   ```sh
   curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/v0.1.8/install-node.sh | sudo sh -s -- --server https://nodes.example.com --code XXXX-XXXX-XXXX --version v0.1.8
   ```
   The script runs as root, so a release server points at its own release tag (`v0.1.8` here) for both the script and `xhaos-node`, never at a moving branch. A development build (its version is not `vX.Y.Z`) uses `main`. The same goes for `flash-node.sh` and the Raspberry Pi files.
2. `install-node.sh` downloads `xhaos-node` for the machine's architecture from the GitHub release and checks it against the release's `SHA256SUMS`, as `install.sh` does. It then creates the `xhaos-node` system user (no password, no sudo) and its root (`/var/lib/xhaos-node/xhaos-work` unless `--root` says otherwise), runs `xhaos-node join` as that user, and installs a systemd service. A release server pins the node to its own release (`--version`). Run it again without `--code` to update. As root it never follows a symlink in the node's folders: it refuses a root or state folder that is a symlink, accepts `--root` only from the characters `A-Za-z0-9._/@+-`, and creates the root as the `xhaos-node` user, so a link planted by a command on the node (which runs as that user) cannot make an update chown or chmod something else.
3. `xhaos-node join` makes an Ed25519 key pair (the private key stays in `/var/lib/xhaos-node/`, readable only by its user), sends the public key, the code and its facts. The server checks the code, burns it, and stores the node with its public key: the public key is the only credential kept on the server. The card turns online.
4. Each later connection answers a fresh challenge from the server by signing it, and the server only talks to a node whose key it stored. The node keeps the server's URL; it only ever connects to that server, over HTTPS (it refuses plain HTTP except to `localhost`, for development).

Removing a node in Xhaos forgets its key: its next connection is refused. `xhaos-node leave` on the machine deletes its key and stops the service.

## Raspberry Pi first boot (no screen, no keyboard)

Raspberry Pi OS based on Debian Trixie (images from November 2025 on) configures itself on first boot with cloud-init, from two files on the card's boot partition: `user-data` and `network-config`. That partition is the FAT one any computer mounts when the card is inserted, so it is the "flash disk with a setup file" we want, with nothing extra to plug in.

**Add node** → **Raspberry Pi first boot** asks for the hostname, the Wi-Fi network, its password and country code, and optionally an SSH public key for yourself, and whether XHAOS USB sticks may change its settings later (**USB stick settings**, off by default; see [USB stick config](#2-usb-stick-config-on-a-running-node-built)). It downloads `user-data` and `network-config`:

- `user-data` creates a user (password login locked; your SSH key if given), sets the hostname, and in `runcmd` runs `install-node.sh` with the server's URL and the join code (and `--usb-config` when the switch is on). A second `runcmd` step deletes the secrets once the node is connected (below).
- `network-config` is always written: wired Ethernet (DHCP), plus the Wi-Fi network when one is given. With no Wi-Fi name the Pi is wired only.
- Both start with the same line, `# xhaos-node-files id=… expires=… node=…`: a random id shared by the pair and the time the join code expires. `flash-node.sh` refuses a pair whose ids differ (one is left over from another node), files whose code has expired, files without the line, and a folder where the browser saved a newer copy with another id beside them (`user-data (1)`, `user-data.txt`), so an old Wi-Fi password or another node's code is never copied. A second copy of the same download (same id) is fine.

Then: flash Raspberry Pi OS Lite (64-bit) with Raspberry Pi Imager or `dd`, copy the two files onto the boot partition (replacing Imager's), put the card in the Pi and power it. A minute or two later the node appears online. The Wi-Fi password is written into the downloaded file only; Xhaos does not store it.

The files hold the Wi-Fi password and a join code, in plain text, on a partition any computer can read. The download says so next to the button. The code is single use and expires after 1 hour, used or not (other join codes keep 15 minutes; flashing a card and a Pi Zero 2 W's first boot take longer).

**Deleting them once the node connects.** `user-data` also writes a small systemd path unit, `xhaos-node-first-boot.path`, which the second `runcmd` step enables after `install-node.sh`. It fires when `/var/lib/xhaos-node/connected` exists, which `xhaos-node` writes after the server's first welcome (not just the join), on this boot or any later one. Its service deletes `user-data` and `network-config` from `/boot/firmware`, touches `/etc/cloud/cloud-init.disabled` (so cloud-init does not take the missing files for a new machine on the next boot and make new SSH host keys), makes the netplan and NetworkManager files root-only (`chmod go-rwx`), syncs, and then removes its own units and script. The network keeps working: netplan renders `/etc/netplan/50-cloud-init.yaml` on every boot without cloud-init. The Wi-Fi password then lives only in that file and in NetworkManager's connection, both readable by root only.

- If the node never connects (wrong code, no network, server down), the files stay, so the card can be fixed without flashing again. The unit stays enabled and clears the card on the boot where the node first connects. Until then the second `runcmd` step's journal line (`journalctl -t xhaos-node`) gives the command to remove them by hand: `sudo rm -f /boot/firmware/user-data /boot/firmware/network-config`.
- A deleted file on an SD card's FAT partition may still be recoverable from the card. Treat a card that has left your hands as holding the Wi-Fi password.
- Not yet checked on a real Pi: that the hostname and the SSH host keys stay the same on the second boot with cloud-init disabled, and that the network comes back (see Open questions).

Older images (Bookworm, `firstrun.sh`) are not supported: use a Trixie image. A Pi Zero 2 W runs the 64-bit image, so `linux/arm64` covers both Pis.

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
curl -fsSL https://raw.githubusercontent.com/devark28/xhaos/v0.1.8/flash-node.sh | sudo sh -s -- --files ~/Downloads
```

- It lists whole disks that are removable, on USB or in an SD slot (`lsblk` `RM=1`, `TRAN=usb`; an `mmcblk*` disk only when it is removable or sysfs says it is an SD card, so a laptop's built-in eMMC never is), up to 512 GiB. Disks with a partition mounted as a system folder (`/`, `/boot`, `/usr`, `/var`, `/home`…) or used as swap, LVM, RAID or encryption are never listed or written. Other mounted partitions (a desktop's automount) are unmounted first. It asks you to type the chosen disk's name (`sdb`) to confirm. `--disk /dev/sdX` skips the menu but not these checks; `--yes` skips typing and needs `--disk`. `--list` shows what could be written, without root.
- It downloads Raspberry Pi OS Lite arm64 (latest, from downloads.raspberrypi.com), checks it against the published SHA-256, and caches it in `/var/cache/xhaos-node/` (the script runs as root), keeping only the newest image. `--image FILE` uses a local image.
- It writes the card, mounts its boot partition, checks it holds `config.txt`, copies `user-data` and `network-config` from `--files` (and compares them), adds an empty `meta-data` if the image has none, syncs and ejects. It refuses a `user-data` whose first line is not `#cloud-config`, a pair that does not belong together or whose code has expired (see the `xhaos-node-files` line above), and says so when a browser saved the files under another name (`user-data (1)`, `.txt`).
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
- It is **off by default**: `install-node.sh --usb-config` (or the **USB stick settings** switch in the Pi form) turns it on, `--no-usb-config` turns it off, and a re-run without either keeps the current choice. A stick labelled `XHAOS` can change the node's Wi-Fi and move it to another server, and it is parsed by a root service.
- With it on, `install-node.sh` (with systemd) adds a udev rule, `/etc/udev/rules.d/90-xhaos-node-usb.rules`. When a FAT or exFAT file system labelled `XHAOS` appears, the rule starts `xhaos-node-usb@<partition>.service`. This is a one-shot service that runs `xhaos-node apply-usb /dev/<partition>` as root. The service also runs at boot when the stick is already plugged in. Only this service runs as root; the node service stays unprivileged.
- `apply-usb` mounts the stick on its own with `nosuid,nodev,noexec`, so nothing on it can run.
- **Wi-Fi:** it writes a NetworkManager connection, `xhaos-usb-<ssid>`, to `/etc/NetworkManager/system-connections/` (readable by root only). The connection is preferred over the node's other networks. It then reloads NetworkManager and connects (waiting up to 45 s). A network out of reach is kept for later, and the node stays on its other networks meanwhile. `wifi_country` uses `raspi-config` where there is one, otherwise `iw reg set`, which lasts until the next restart.
- **Joining:** it joins with the code, retrying for up to 2 minutes while the network comes up, and holds the new key in memory meanwhile. It stops at once when the server refuses the code. Only after the join succeeds does it stop the node service, replace the key and settings, and start the service again.
- **Root and the node's files:** the state folder belongs to the `xhaos-node` user, and so to any command an agent runs on an unsandboxed node, so `apply-usb` trusts nothing in it. It opens the folder and `node.json` without following symlinks. It refuses a `node.json` whose folder is not a clean absolute path that is either a real folder of the `xhaos-node` user or missing and inside `/var/lib/xhaos-node`, and never creates or chowns that folder (the node service makes it). It writes the new key and settings beside the old ones with `O_CREAT|O_EXCL|O_NOFOLLOW`, hands them to `xhaos-node` with `fchown` on the open file, and renames them into place, so a planted symlink is replaced, never written through. A failed join leaves the old key. It renames `node.json` before `node.key`; if the second rename fails, the stick's log says the join was only partly saved and that the node needs a new code (plug the stick in again, or `xhaos-node leave` and join by hand). The node keeps its folder, devices and sandbox mode. The node's old entry, on its old server, stays there as offline until it is removed.
- **Result:** it writes `xhaos-node-result.txt` back to the stick: the time, what it did, any errors, and the hostname, IP addresses, active connections, the node it is joined as and whether its service runs. Plug the stick in, wait a minute, unplug it and read the file on a computer. There is no LED signal yet.
- **Secrets:** whatever happened, it rewrites `xhaos-node.conf` with every setting commented out and the password and the code removed. Plugging the stick in again only writes a new result. The password then lives only in NetworkManager's root-only file.
- `install-node.sh --no-usb-config` removes the rule and the service. Anyone who can plug in a stick can move the node to their own server. For a Pi that adds little, since anyone holding it can already take its card; for a build PC in a shared room, leave it off.

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

- Commands and file operations stay inside the node root. Commands run under Landlock (the same sandbox package as the server: read and run system files, write only the root and a temp folder). On a kernel without Landlock they run under **bubblewrap** instead (see [Bubblewrap](#bubblewrap-kernels-without-landlock)). **Without either, the node refuses to run commands.** Only an owner on the machine itself can lift that for that node, with `xhaos-node join --unsandboxed` (or `install-node.sh --unsandboxed`); the server cannot ask for it. The node's card shows the sandbox: "Landlock N" or "bubblewrap", and "unsandboxed" or "no sandbox: runs no commands" in red. `xhaos-node status` and `xhaos-node sandbox-check` print it on the machine.
- Devices are opt-in on the node: `--device /dev/video0 --device /dev/ttyUSB0` (or the user's group membership, `video`, `dialout`, when not sandboxed). Without them a sandboxed command cannot open hardware.
- No `sudo`: the `xhaos-node` user has none. Installing packages on a node is the owner's job.
- Commands have a time limit; jobs have an output cap (the newest 10 MB are kept).

### Bubblewrap (kernels without Landlock)

Raspberry Pi OS's kernels have no Landlock (`CONFIG_SECURITY_LANDLOCK` is not set in the rpi defconfigs 6.6 to 6.19; on a Zero 2 W with trixie and 6.18, `/sys/kernel/security/lsm` is just `capability`). They do have user namespaces and seccomp, so `xhaos-node` falls back to bubblewrap (`bwrap`, Debian package `bubblewrap`, about 150 kB), which `install-node.sh` installs with apt when the kernel has no Landlock.

At start the node picks: Landlock when the kernel has it, else bubblewrap when `bwrap -- true` works for its user, else none (commands are refused, and the card and the log say why: bwrap missing, `max_user_namespaces` 0, AppArmor's user namespace restriction). `xhaos-node run --sandbox landlock|bwrap` forces one (to try bubblewrap on a machine that has Landlock).

Under bubblewrap a command gets the same policy as under Landlock, enforced by mounts instead: a new mount namespace that holds only the policy's folders (system folders and the toolchains on `PATH` read-only; the node root, the node's cache and the command's temp folder writable), a private `/tmp`, a read-only `/dev` with only `null`, `zero`, `full`, `random`, `urandom`, `tty`, the pseudo-terminals, a private writable `/dev/shm` and the devices given with `--device` (no new files can be made there, as under Landlock), and a `/proc` of its own pid namespace (it cannot see or signal the node or other processes). The rest of the machine, such as `/var/lib/xhaos-node` with the node's key, `/home` and `~/.ssh`, does not exist inside. The same seccomp filter refuses Unix sockets. Each command also gets a new session (no controlling terminal, so it cannot push keystrokes into the terminal `xhaos-node run` was started from), dies with the node, and, with bwrap 0.8 or later, cannot make user namespaces of its own. `/etc/resolv.conf`'s target is readable when it is a link into `/run` (systemd-resolved, NetworkManager), so names resolve. The network is shared, as under Landlock. Commands holding secrets need Landlock, so they are refused under bubblewrap (nodes do not take secrets today).

`install-node.sh` runs its sandbox check the way the service runs (`systemd-run` with the unit's user, environment, `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`), so the answer it prints is the service's, and it prints the service's own `sandbox:` line after starting it.

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

Approvals work as for `bash`: under manual review a node command waits for Allow, and the card shows the node's name with the command. Read-only agents may list and read, not run or copy onto a node. Dangerously allow covers node calls like any other: `node_run` and `node_copy` then run without asking. The check that still stops a dangerously allowed agent's `bash` command that cannot be undone (`danger.CheckCommand`) does not look at `node_run` commands yet. Output comes back with the same 30,000-byte cap as `bash`.

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

Before a node has proven itself (join or signed hello), the server reads at most 64 KiB per frame (`wire.MaxHello`; a node with a long job list sends it right after the welcome instead), at most 32 such sockets are open at once and 4 per address (an IPv6 /64 counts as one address; more get `503`), and each must finish its join or hello within 10 seconds. Only join codes can be guessed, so only wrong codes count against an address: after 3, joins from it wait (1 s, doubling up to a minute). A node with a good key is never held back by them (a shared NAT), and its hello does not clear them.

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

- Raspberry Pi OS's kernel has no Landlock (checked on a Zero 2 W, trixie, 6.18): Pis use bubblewrap. Still to check on a real Pi: bubblewrap inside the node's systemd service.
- First boot now deletes `user-data` and `network-config` and disables cloud-init once the node is connected (see [Raspberry Pi first boot](#raspberry-pi-first-boot-no-screen-no-keyboard)). Still to check on a real Pi: the hostname, the SSH host keys and the network stay the same on the second boot.
- USB boot on our hardware: which of our sticks the Zero 2 W boots from, and whether the Pi 5's bootloader on our units has USB in `BOOT_ORDER` (it is in the default). Check this before building the installer stick.
- Should a node be shareable between workspaces? This design says no: one node, one workspace.

## Later (known, not fixed yet)

- **Keepalive during slow copies.** The hub delivers a node's frames to the waiting request one at a time; during a slow node-to-node copy a blocked delivery can stop the connection's reader, so a ping's pong is not read in time and the link is dropped. Plan: per-call credits (the receiver grants chunks) so delivery never blocks the reader.
- **Remove during a handshake.** Removing a node while it is in the middle of its hello can leave that connection open. Harmless: every request checks the database, so the removed node gets no work, and it is dropped on its next reconnect.
- **Dangerously allow and `node_run`.** See [For agents](#for-agents): the irreversible-command check covers only `bash`. Extending it to `node_run` (same check on the command) is planned.
