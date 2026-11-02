# Backups

How Examiner backs up its databases and configuration on `examiner-01`
(ТЗ п. 3.7, items 1, 2, 3 and 5). To restore, see [restore.md](restore.md).

## What a backup is

`examiner backup` (and the **Backups** admin page) writes two files per
instance into `~/examiner/backups/<lang>/` on the host:

| File | Content |
| ---- | ------- |
| `examiner-<lang>-YYYYmmdd-HHMMSS.db.gz` | The whole database: users, questions, sessions, answers, scores, grades |
| `examiner-<lang>-YYYYmmdd-HHMMSS.config.tar.gz` | `examiner-<lang>.yaml`, `questions-<lang>.json`, the Quadlet files `examiner-<lang>.container` and `examiner-<lang>-data.volume`, and the `Caddyfile` |

The time in the name is UTC. Admin-page backups have no configuration
archive: the running instance cannot see the host's configuration files.

Each database backup is:

1. **A consistent snapshot**, taken with `VACUUM INTO` while the server
   keeps running. Copying `examiner.db` instead is unsafe in WAL mode:
   recent changes live in `examiner.db-wal`, and a copy made during a write
   can be inconsistent.
1. **Checked** with `PRAGMA integrity_check` before it is kept. A failed
   check fails the backup, with a non-zero exit code.
1. **Compressed** and written under a temporary name, then renamed, so a
   file with a final name is always complete.
1. **Readable by its owner only** (mode `0600`). Backups hold password
   hashes and student answers.

**Rotation.** After each backup, only the newest 14 backups of the
instance are kept, together with their configuration archives. Change
`--keep` in `examiner-backup@.service`, or `backup-keep` for the admin
page.

## Secrets are not backed up

`examiner-<lang>.env` (LLM key, admin password) is **not** in the
backups, and `examiner backup` refuses to archive any `*.env` file.
Backups are copied off the host, and an off-host copy of the LLM key is
a liability. Keep the secrets in the team's password manager. After a
full host loss, recreate the `.env` files from
`deploy/examiner-<lang>.env.example` and the password manager.

## When backups are made

| When | How | Where it is set up |
| ---- | --- | ------------------ |
| Every 6 hours (RPO 6 h) | `examiner-backup@<lang>.timer` runs `examiner-backup@<lang>.service` | `deploy/examiner-backup@.timer` |
| Before an exam, by hand | **Admin → Backups → Create a backup now**, or `task backup` from a workstation | admin page; `Taskfile.yml` |
| Before every deploy | `task deploy` and `task deploy-quick` run both backup services first; a failed backup stops the deploy | `Taskfile.yml` (`pre-deploy-backup`) |

`SKIP_BACKUP=true task deploy-quick` skips the backup before a deploy.
Use it only when no backup can be made, for example to deploy a fix for
an instance that does not start. The timer keeps working independently.

## Setting up the host (once)

```bash
# On examiner-01, from a checkout of the repository:
mkdir -p ~/examiner/backups/en ~/examiner/backups/ru

# The instances mount ~/examiner/backups/<lang> for the admin page.
cp deploy/examiner-en.container deploy/examiner-ru.container ~/.config/containers/systemd/

# The backup service and timer are plain user units, not Quadlet files.
mkdir -p ~/.config/systemd/user
cp deploy/examiner-backup@.service deploy/examiner-backup@.timer ~/.config/systemd/user/

systemctl --user daemon-reload
systemctl --user restart examiner-en examiner-ru
systemctl --user enable --now examiner-backup@en.timer examiner-backup@ru.timer
```

Check it once after setting it up:

```bash
systemctl --user start examiner-backup@ru.service      # waits until done
journalctl --user -u examiner-backup@ru.service -n 5 -o cat
# Backup: /data/backups/examiner-ru-20261103-060012.db.gz (35089 bytes, integrity ok)
# Config: /data/backups/examiner-ru-20261103-060012.config.tar.gz
systemctl --user list-timers 'examiner-backup@*'        # next run times
```

Then open **Admin → Backups** on both instances: the backup is listed
there, and **Create a backup now** must add another one.

### How the service runs

The backup service runs `podman run` with the **same image** as the
instance (the `Image=` line of its Quadlet file) and the image's default
user, the user the instance runs as. As a result:

- it can read the database in the volume `systemd-examiner-<lang>-data`
  and write files the instance can read and rotate;
- `--security-opt label=disable` lets it open a volume that the instance
  mounted with `:Z`;
- it works whether the instance is running or not.

The backup directory belongs to the container user (the `:U` mount
option). On the host, look at it through that user's namespace:

```bash
podman unshare ls -l ~/examiner/backups/ru
```

## Copies off the host

A copy on `examiner-01` does not survive the loss of the host. Copy the
backups to another machine regularly, at least before and after each
exam:

```bash
task backup-pull                 # copies everything to ./backups (git-ignored)
task backup-pull DEST=/Volumes/backup/examiner
```

From there, use any sync tool, for example
`rclone copy ./backups remote:examiner-backups`. The files hold personal
data, so the target must not be public.

## Commands

| Command | What it does |
| ------- | ------------ |
| `task backup` | Back up both instances now and print the new file names |
| `task backup-list` | List the backups on the host |
| `task backup-pull [DEST=dir]` | Copy all backups to a workstation |
| `examiner backup --db F --out-dir D --name N [--keep K] [--config F ...]` | The subcommand the services and tasks run |
| `examiner stats --db F` | Count records, to compare a backup with the original |
