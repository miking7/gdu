# Scheduling periodic scans

gdu is a scanner, not a scheduler — use your operating system's scheduler to run
`gdu --save-scan` on a schedule. **This guide assumes you want a whole-disk scan as `root`**, which is
the usual case: only root can see every user's files, so a root scan gives complete totals. Running as
your own user instead is covered briefly at the [end](#per-user-non-root-scans).

## How the snapshot ends up owned by *you* (not root)

A root scan writes everything as `root`. gdu fixes this **without any manual `chown`**: pass
**`--owner youruser`** and it writes the snapshot into that user's `~/.gdu-scans` and hands every file
it creates back to them. That one flag is all the units below need — replace `youruser` with your
login name.

> Under the hood `--owner` looks the user up (home directory + numeric IDs) and `chown`s output to
> them; this works on both macOS and Linux. Equivalently you can set `SUDO_USER` (and optionally
> `SUDO_UID`/`SUDO_GID`) in the unit's environment — gdu honours those for real `sudo` runs too — but
> `--owner` is the tidy one-flag form and is what this guide uses. Ownership hand-back only happens
> when the scan runs as root; running as your own user, the files are already yours.

General tips for any scheduler: always use the **absolute path** to `gdu` (schedulers have a minimal
`PATH`), and pass `-np` (non-interactive, no progress bar).

---

## Linux — systemd timer (recommended)

Preferred over cron: it catches up runs missed while the machine was off (`Persistent=true`), logs to
the journal, and shows the schedule with `systemctl list-timers`. You create two files — a *service*
(what to run) and a *timer* (when).

**1. Create the service.** Run `sudo nano /etc/systemd/system/gdu-scan.service` and paste:

```ini
[Unit]
Description=gdu whole-disk usage snapshot

[Service]
Type=oneshot
# Replace youruser with your login name; --owner hands the snapshot back to them.
ExecStart=/usr/local/bin/gdu -np --owner youruser --save-scan /

# Be a good neighbour: lowest CPU + idle disk priority.
Nice=19
IOSchedulingClass=idle
```

Save and exit nano with **Ctrl-O**, **Enter**, **Ctrl-X**.

**2. Create the timer.** Run `sudo nano /etc/systemd/system/gdu-scan.timer` and paste:

```ini
[Unit]
Description=Run gdu-scan.service on a schedule

[Timer]
OnCalendar=*-*-* 03:00:00       # daily at 03:00
# For every 6 hours instead, comment the line above and use:
# OnCalendar=00/6:00:00         # 00:00, 06:00, 12:00, 18:00
Persistent=true                 # run a missed scan after the machine was off
RandomizedDelaySec=900          # spread the start by up to 15 min

[Install]
WantedBy=timers.target
```

**3. Enable and test:**

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now gdu-scan.timer   # start now + on every boot
systemctl list-timers gdu-scan.timer         # when it last/next runs
sudo systemctl start gdu-scan.service        # run one scan right now, to test
journalctl -u gdu-scan.service               # logs / exit status
```

After it runs, check `ls -l ~/.gdu-scans/` — the `scan_*.parquet` file should be owned by **you**, not
root.

### Linux — cron (simpler alternative)

cron is everywhere and one line, but it has **no catch-up** for missed runs and **no built-in
logging**. Use a system drop-in — run `sudo nano /etc/cron.d/gdu` and paste (the `root` field means
"run as root"; replace `youruser`):

```cron
# m h dom mon dow user  command
30 2 * * *  root  /usr/local/bin/gdu -np --owner youruser --save-scan / >> /var/log/gdu-scan.log 2>&1
```

For every 6 hours use `0 */6 * * *` instead of `30 2 * * *`.

---

## macOS — launchd (root LaunchDaemon)

macOS uses **launchd** (cron is deprecated and gated behind "Login Items" on macOS 15+). A
**LaunchDaemon** runs as root on a schedule, independent of login. Two parts: grant Full Disk Access
*first*, then install the daemon.

### Step 1 — Give gdu Full Disk Access (do this first)

A whole-disk scan reaches TCC-protected places (Mail, Messages, other users' homes, …). Without Full
Disk Access (FDA), gdu silently hits "operation not permitted" there and under-reports. A scheduled
job has no Terminal to borrow FDA from, so you grant it to the **gdu binary itself**.

FDA is pinned to the *exact* binary, and Homebrew upgrades replace the binary (breaking the grant), so
first copy gdu to a stable path you control. `/usr/local/sbin` is a good spot — it sits outside
Homebrew's tree (Intel Homebrew uses `/usr/local/bin`; Apple Silicon uses `/opt/homebrew`), so nothing
overwrites your copy — but it often doesn't exist yet (`cp` won't create it), so make it first:

```sh
sudo mkdir -p /usr/local/sbin
sudo cp "$(which gdu)" /usr/local/sbin/gdu
sudo chown root:wheel /usr/local/sbin/gdu
```

Then grant FDA to `/usr/local/sbin/gdu`:

1. Open **System Settings → Privacy & Security → Full Disk Access**.
2. Click **+** and authenticate.
3. In the file picker press **⌘⇧G**, type **`/usr/local/sbin/gdu`**, press **Enter**, then **Open**.
   (The binary may *not* visibly appear in the list — the grant is still recorded.)
4. Make sure its toggle is **on**.

> **Maintenance caveat (important):** the grant is tied to the binary's contents. **Every time you
> update gdu, re-copy it to `/usr/local/sbin/gdu` and re-add it to Full Disk Access** (remove the old
> entry and add it again). There is no way around this for a locally built / Homebrew binary.
>
> **If the picker refuses to add the binary** (reported on the very newest macOS), the fallback is to
> wrap gdu in a small **Automator "Application"** (one "Run Shell Script" action calling
> `/usr/local/sbin/gdu …`) and grant Full Disk Access to that `.app` instead, then have the daemon
> launch the app. Test before relying on it.

### Step 2 — Create the LaunchDaemon

Run `sudo nano /Library/LaunchDaemons/local.gdu-scan.plist` and paste (daily at 03:00; replace
`youruser` with your login name):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.gdu-scan</string>

    <!-- --owner hands the snapshot back to youruser (it runs as root otherwise). -->
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/sbin/gdu</string>
        <string>-np</string>
        <string>--owner</string>
        <string>youruser</string>
        <string>--save-scan</string>
        <string>/</string>
    </array>

    <!-- Daily at 03:00. For every 6 hours, replace this whole dict with the array shown below. -->
    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key><integer>3</integer>
        <key>Minute</key><integer>0</integer>
    </dict>

    <key>StandardErrorPath</key><string>/var/log/gdu-scan.err</string>
    <key>StandardOutPath</key><string>/var/log/gdu-scan.out</string>
</dict>
</plist>
```

Save with **Ctrl-O**, **Enter**, **Ctrl-X**.

> **If your gdu path or `--owner` name contains `&`, `<`, or `>`** (e.g. a folder named
> `Test & Dev`), you must XML-escape it inside the plist: write `&` as `&amp;`, `<` as `&lt;`, `>` as
> `&gt;`. An unescaped character makes the plist invalid XML, and launchd **silently refuses to load
> it** — no run, no snapshot, no log, no error. After editing, sanity-check with
> `plutil -lint /Library/LaunchDaemons/local.gdu-scan.plist` (it should print `OK`). The
> `/usr/local/sbin/gdu` path above needs no escaping.

For **every 6 hours**, swap the `StartCalendarInterval` dict for an array of times (pinned to the
clock; on a laptop that sleeps through a time, that run is skipped, not caught up):

```xml
    <key>StartCalendarInterval</key>
    <array>
        <dict><key>Hour</key><integer>0</integer><key>Minute</key><integer>0</integer></dict>
        <dict><key>Hour</key><integer>6</integer><key>Minute</key><integer>0</integer></dict>
        <dict><key>Hour</key><integer>12</integer><key>Minute</key><integer>0</integer></dict>
        <dict><key>Hour</key><integer>18</integer><key>Minute</key><integer>0</integer></dict>
    </array>
```

### Step 3 — Set permissions, load, and test

The plist must be owned `root:wheel` and mode `644` or launchd refuses it:

```sh
sudo chown root:wheel /Library/LaunchDaemons/local.gdu-scan.plist
sudo chmod 644        /Library/LaunchDaemons/local.gdu-scan.plist

plutil -lint /Library/LaunchDaemons/local.gdu-scan.plist   # want: "OK"
sudo launchctl bootstrap system /Library/LaunchDaemons/local.gdu-scan.plist   # load it
sudo launchctl kickstart -k system/local.gdu-scan                            # run once now, to test
```

Verify it worked:

```sh
cat /var/log/gdu-scan.err            # should be empty / no "operation not permitted"
ls -l ~/.gdu-scans/                  # the new snapshot should be owned by you, not root
```

If you see permission errors on protected paths, FDA isn't taking effect — re-check Step 1.

### Changing the schedule later

launchd has **no "reload"** — editing the plist on disk does *not* update the running daemon, and
`kickstart` only re-runs the job, it does not re-read the file. After any edit you must unload, then
load again:

```sh
sudo launchctl bootout system/local.gdu-scan                                   # unload the old copy
sudo launchctl bootstrap system /Library/LaunchDaemons/local.gdu-scan.plist    # load the edited one
```

If you run `bootstrap` *without* `bootout` first, launchd refuses with **`Bootstrap failed: 5:
Input/output error`** — that just means it's still loaded from before; `bootout` it and run `bootstrap`
again. (If `bootout` instead reports `Boot-out failed: 3: No such process`, it wasn't loaded — then
check `plutil -lint` and the `root:wheel` / `644` permissions.) To remove the daemon entirely, run only
the `bootout` line and delete the plist.

---

## Per-user (non-root) scans

If you only care about your *own* files (and don't need other users' data), skip root entirely — then
there's no ownership dance at all, because the job already runs as you.

- **Linux:** put the same two unit files under `~/.config/systemd/user/` (drop `--owner` and the
  `Nice` line), then `systemctl --user enable --now gdu-scan.timer`. To run when you're *not* logged
  in, enable lingering once: `sudo loginctl enable-linger "$USER"`.
- **macOS:** use a **LaunchAgent** at `~/Library/LaunchAgents/local.gdu-scan.plist` (same plist, minus
  `--owner`), loaded with `launchctl bootstrap gui/$(id -u) <plist>`. You still grant **Full Disk
  Access to the gdu binary** (Step 1) if you want it to see your protected folders (Mail, Messages, etc.).

A user scan won't include other users' files or some system areas that need root — that's the
trade-off for skipping the ownership setup.
