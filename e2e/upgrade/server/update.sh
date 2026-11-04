# update.sh TO updates the control plane the way an operator does
# (`dock update apply TO`, which waits; ADR 0028) and keeps the updater's log.
source /root/upg/lib.sh
set +e
TO=${1:?version}
d update --json > $EV/update-check.json 2>&1
d update | head -20
start=$(date +%s)
d update apply "$TO" 2>&1 | tee $EV/update-apply.txt
echo "update took $(( $(date +%s) - start ))s" | tee -a $EV/update-apply.txt
journalctl -u dock-update --no-pager -o cat > $EV/dock-update.log 2>&1
wait_api
d version
