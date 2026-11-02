#!/usr/bin/env bash
# Restores one Examiner instance on the host from a backup made by
# examiner-backup@<lang>.service or the admin page. See docs/ops/restore.md.
#
#   restore.sh <en|ru> <backup file name>
#
# From a workstation: task restore LANG=ru BACKUP=examiner-ru-20261103-060000.db.gz
#
# Steps: print the current counts, stop the service, restore (the backup is
# checked first; the current database is moved aside, not deleted), print
# the restored counts, start the service and check /healthz and /login.
# The service is started again whatever happens, so a failed restore
# leaves the old database running.
set -euo pipefail

usage="usage: restore.sh <en|ru> <backup file name>"
lang=${1:?$usage}
backup=${2:?$usage}
case "$lang" in
en) port=8080 prefix= ;;
ru) port=8081 prefix=/ru ;;
*)
	echo "unknown instance '$lang': use en or ru" >&2
	exit 2
	;;
esac
if [[ $backup == */* ]]; then
	echo "give the file name only; it is looked up in ~/examiner/backups/$lang" >&2
	exit 2
fi

service=examiner-$lang
backups=$HOME/examiner/backups/$lang
image=$(sed -n 's/^Image=//p' "$HOME/.config/containers/systemd/$service.container")
# The backups belong to the container user; look at them inside its namespace.
if ! podman unshare test -f "$backups/$backup"; then
	echo "no such backup: $backups/$backup (task backup-list shows them)" >&2
	exit 1
fi

# The image's default user owns the database, so the restored file keeps
# the owner the service expects.
examiner() {
	podman run --rm --pull=never --security-opt label=disable \
		--volume "systemd-$service-data:/data/db" \
		--volume "$backups:/data/backups:ro" \
		"$image" "$@"
}

echo "== Counts in the current database"
examiner stats --db /data/db/examiner.db || echo "(the current database cannot be read)"

echo "== Stopping $service"
systemctl --user stop "$service"
trap 'echo "== Starting $service"; systemctl --user start "$service"' EXIT

echo "== Restoring $backup"
examiner restore --db /data/db/examiner.db --backup "/data/backups/$backup"

echo "== Counts in the restored database"
examiner stats --db /data/db/examiner.db

trap - EXIT
echo "== Starting $service"
systemctl --user start "$service"

echo "== Smoke check"
curl -fsS --retry 15 --retry-connrefused --retry-delay 2 "http://127.0.0.1:$port$prefix/healthz"
curl -fsS -o /dev/null -w "login page: HTTP %{http_code}\n" "http://127.0.0.1:$port$prefix/login"
echo "Restored $service from $backup."
echo "The replaced database stays in the volume as examiner.db.pre-restore-<UTC time>."
