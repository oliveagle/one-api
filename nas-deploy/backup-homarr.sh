#!/bin/bash
# Backup Homarr SQLite DB from OpenWrt to ~/.agents/backups/homarr/
# Usage: ./backup-homarr.sh [--keep N]  (default keep 7 backups)

set -euo pipefail

BACKUP_DIR="$HOME/.agents/backups/homarr"
REMOTE_HOST="oleOpenWrt_249"
REMOTE_DB="/mnt/mmcblk2p4/homarr/data/database.sqlite"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
KEEP="${1:-7}"

mkdir -p "$BACKUP_DIR"

echo "==> Backing up Homarr SQLite DB from OpenWrt..."
echo "    Source: $REMOTE_HOST:$REMOTE_DB"
echo "    Target: $BACKUP_DIR/homarr.db.$TIMESTAMP"

# Create local backup on OpenWrt first
ssh "$REMOTE_HOST" "cp $REMOTE_DB ${REMOTE_DB}.bak.$TIMESTAMP" 2>/dev/null || true

# Copy to Mac
scp "$REMOTE_HOST:$REMOTE_DB" "$BACKUP_DIR/homarr.db.$TIMESTAMP"

echo "==> Backup complete: $(ls -lh $BACKUP_DIR/homarr.db.$TIMESTAMP | awk '{print $5}')"

# Clean up old backups
count=$(ls -1 "$BACKUP_DIR"/homarr.db.* 2>/dev/null | wc -l)
if [ "$count" -gt "$KEEP" ]; then
    echo "==> Cleaning up old backups (keeping $KEEP)..."
    ls -1t "$BACKUP_DIR"/homarr.db.* | tail -n +$((KEEP+1)) | xargs rm -f
fi

echo "==> Done. Backups in $BACKUP_DIR:"
ls -1lh "$BACKUP_DIR"/
