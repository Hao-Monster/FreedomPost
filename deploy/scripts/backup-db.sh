#!/usr/bin/env sh
set -eu
umask 077

BACKUP_DIR="${BACKUP_DIR:-runtime/backups/db}"
DATABASE_URL="${DATABASE_URL:?DATABASE_URL is required}"

mkdir -p "$BACKUP_DIR"
FINAL_DUMP="$BACKUP_DIR/freedompost_$(date +%F).dump"
TMP_DUMP="${FINAL_DUMP}.tmp.$$"
trap 'rm -f "$TMP_DUMP"' EXIT INT TERM

pg_dump -Fc "$DATABASE_URL" > "$TMP_DUMP"
mv "$TMP_DUMP" "$FINAL_DUMP"
trap - EXIT INT TERM

find "$BACKUP_DIR" -name "freedompost_*.dump" -mtime +30 -delete
