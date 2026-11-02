# Restoring from a backup

Runbook for restoring one Examiner instance on `examiner-01` (ТЗ п. 3.7,
items 4 and 6). Backups are described in [backup.md](backup.md).

| Target | Value |
| ------ | ----- |
| **RPO** (data that can be lost) | At most 6 hours: the timer interval. Make a manual backup right before an exam to bring it to minutes. |
| **RTO** (time to restore) | About 5 minutes for one instance: 1 minute to pick the backup, under a minute for the restore and restart, the rest for checks. The database step itself took 0.1 s in the [restore test](../testing/restore-test-2026-11-03.md). |

## Before you start

- Decide which instance: `en` (port 8080, path `/`) or `ru` (port 8081,
  path `/ru`).
- Tell users the instance will be unavailable for a few minutes.
  Students in an exam lose what they type during the restore and
  everything saved after the backup was taken.
- If the instance still runs, make a backup of the current state first
  (`task backup`). The restore also keeps the replaced database, but a
  backup can be copied off the host.

## 1. Choose the backup

```bash
task backup-list
#   /home/…/examiner/backups/ru:
#   -rw------- 1 … 35089 Nov  3 06:00 examiner-ru-20261103-060012.db.gz
#   -rw------- 1 …  2210 Nov  3 06:00 examiner-ru-20261103-060012.config.tar.gz
```

The time in the name is UTC.

## 2. Restore

```bash
task restore LANG=ru BACKUP=examiner-ru-20261103-060012.db.gz
```

This runs [`scripts/restore.sh`](../../scripts/restore.sh) on the host.
Expected output:

```text
== Counts in the current database
users                    124
questions                56
…
== Stopping examiner-ru
== Restoring examiner-ru-20261103-060012.db.gz
Restored /data/db/examiner.db from /data/backups/examiner-ru-20261103-060012.db.gz (integrity ok)
Previous database kept as /data/db/examiner.db.pre-restore-20261103-091502
== Counts in the restored database
users                    123
questions                56
…
== Starting examiner-ru
== Smoke check
ok
login page: HTTP 200
Restored examiner-ru from examiner-ru-20261103-060012.db.gz.
```

What the script does, in order:

1. Prints the counts of the current database (`examiner stats`).
1. Stops the service: `systemctl --user stop examiner-ru`.
1. Runs `examiner restore` in a container with the instance's image and
   user.
   - It unpacks the backup **next to** the database and runs
     `PRAGMA integrity_check` on it. If the backup is damaged, it stops
     here and the database is not touched.
   - It moves the current `examiner.db`, `-wal` and `-shm` aside as
     `examiner.db.pre-restore-<UTC time>` (+ `-wal`, `-shm`). Nothing is
     deleted.
   - It puts the backup in place. No stale `-wal` or `-shm` is left next
     to it.
1. Prints the counts of the restored database.
1. Starts the service and checks `/healthz` and `/login`.

If any step fails, the script still starts the service again. A failed
restore therefore leaves the old database running.

### Without Task

Run the same script on the host:

```bash
ssh examiner-01 'bash -s' -- ru examiner-ru-20261103-060012.db.gz < scripts/restore.sh
```

## 3. Check

1. The counts after the restore match what you expect from the backup.
   To compare with the backup itself, restore it on a workstation as in
   [Testing a backup on another machine](#testing-a-backup-on-another-machine).
1. Log in as a student, a teacher and the admin.
1. As a teacher, open a session in **Review**.
1. As the admin, open **Admin → Backups**.

## 4. Configuration (only if it was lost)

The configuration archive has the files listed in
[backup.md](backup.md). Unpack it, compare, and copy back only what is
missing or wrong:

```bash
mkdir -p /tmp/examiner-config
podman unshare tar -xzf ~/examiner/backups/ru/examiner-ru-20261103-060012.config.tar.gz -C /tmp/examiner-config
diff /tmp/examiner-config/examiner-ru.yaml ~/examiner/examiner-ru.yaml
# copy what is needed, then:
systemctl --user daemon-reload && systemctl --user restart examiner-ru
```

`examiner-ru.env` is not in the backups. Recreate it from the
password manager (see [backup.md](backup.md#secrets-are-not-backed-up)).

## Undoing a restore

The replaced database is a valid restore source. It keeps the changes
that were still in its `-wal` file. Run the restore again with its file
name, from the data volume:

```bash
ssh examiner-01
systemctl --user stop examiner-ru
image=$(sed -n 's/^Image=//p' ~/.config/containers/systemd/examiner-ru.container)
podman run --rm --pull=never --security-opt label=disable \
  --volume systemd-examiner-ru-data:/data/db "$image" \
  restore --db /data/db/examiner.db --backup /data/db/examiner.db.pre-restore-20261103-091502
systemctl --user start examiner-ru
```

When the restored instance has been running long enough to be sure,
delete the `examiner.db.pre-restore-*` files from the volume. They
take space and hold personal data:

```bash
podman run --rm --pull=never --security-opt label=disable --entrypoint /bin/sh \
  --volume systemd-examiner-ru-data:/data/db "$image" \
  -c 'rm -v /data/db/examiner.db.pre-restore-*'
```

## Testing a backup on another machine

A backup is only known to work once it has been restored. Repeat this
before each exam session and after changes to the backup setup. The
first run is recorded in
[restore-test-2026-11-03.md](../testing/restore-test-2026-11-03.md).

1. Make a fresh backup and note the counts of the live database at the
   same time:

   ```bash
   task backup
   ssh examiner-01 'image=$(sed -n "s/^Image=//p" ~/.config/containers/systemd/examiner-ru.container);
     podman run --rm --pull=never --security-opt label=disable \
       --volume systemd-examiner-ru-data:/data/db "$image" stats --db /data/db/examiner.db'
   ```

1. Copy the backups to the test machine and restore the new one into an
   empty directory:

   ```bash
   task backup-pull
   ./bin/examiner restore --db stand/examiner.db --backup backups/ru/examiner-ru-20261103-060012.db.gz
   ./bin/examiner stats --db stand/examiner.db   # must match the counts above
   ```

   Records created between the backup and the `stats` call make the
   counts differ. Take both while the instance is quiet.

1. Start a server on the restored database from a checkout of the
   repository, so it finds `schema/`:

   ```bash
   ./bin/examiner serve --db stand/examiner.db --base-path /ru --secure-cookies=false \
     --questions questions/physics_ru.json --llm-url … --llm-key …
   ```

1. Log in as each role and check the main pages, as in step 3.
1. Record the results as a new `docs/testing/restore-test-YYYY-MM-DD.md`.

## Troubleshooting

| Symptom | Cause and fix |
| ------- | ------------- |
| `no such backup` | Give the file name only, as `task backup-list` prints it. |
| `integrity_check failed` or `unexpected EOF` | The backup is damaged; nothing was changed. Use an older backup, or the off-host copy. |
| `Error: open database` in the backup service | The volume name changed or the instance never started; check `podman volume ls`. |
| `/healthz` does not answer after the restart | `journalctl --user -u examiner-ru -n 50`; undo the restore as above if the new database is the cause. |
| `permission denied` on the database after a manual copy | The file must belong to the container user; restore with `examiner restore` in a container, as above, instead of copying. |
