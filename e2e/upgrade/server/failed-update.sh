# failed-update.sh exercises the updater's own rollback (ADR 0028): it
# publishes a fake release vNEXT in a release directory ([updates] source)
# whose binary is the current one, except that `dock serve` exits at once,
# so the health check fails. `dock update apply vNEXT` must then put the
# current binary, units and database back and come up on the old version
# with the data. Run after an update, on the current version.
source /root/upg/lib.sh
set +e
R=$EV/failed-update.txt
: > $R
CUR=$(curl -fsS $DOCK_URL/api/v1/version | jq -r .version)
NEXT=${1:-v0.99.0}
SRC=/srv/upg-releases
mkdir -p $SRC/$NEXT /opt/upg
install -m 0755 /usr/local/bin/dock /opt/upg/dock-real
cat > $SRC/$NEXT/dock-linux-amd64 <<EOF
#!/bin/sh
# Fake $NEXT: the real $CUR, except that the daemon never comes up.
case "\$1" in
  --version|version) /opt/upg/dock-real "\$@" | sed 's/$CUR/$NEXT/g' ;;
  serve) echo "fake $NEXT: serve fails on purpose" >&2; exit 1 ;;
  *) exec /opt/upg/dock-real "\$@" ;;
esac
EOF
chmod 0755 $SRC/$NEXT/dock-linux-amd64
tar -czf $SRC/$NEXT/dock-install-$NEXT.tar.gz -C /root/rel deploy
printf 'Fake release for the upgrade scenario: serve fails.\n' > $SRC/$NEXT/notes.md
(cd $SRC/$NEXT && sha256sum dock-linux-amd64 dock-install-$NEXT.tar.gz > SHA256SUMS)
chmod -R a+rX $SRC /opt/upg

cp /etc/dock/config.toml $U/config.toml.orig
grep -q '^\[updates\]' /etc/dock/config.toml || printf '\n[updates]\n' >> /etc/dock/config.toml
sed -i "/^\[updates\]/a source = \"$SRC\"" /etc/dock/config.toml
systemctl restart dock && wait_api

before=$(db "SELECT count(*) FROM audit_events")
start=$(date +%s)
d update apply $NEXT 2>&1 | tee -a $R
echo "apply returned after $(( $(date +%s) - start ))s" | tee -a $R
wait_api
journalctl -u dock-update --no-pager -o cat | tail -30 >> $R
echo "version after: $(curl -fsS $DOCK_URL/api/v1/version)" | tee -a $R
cat /var/lib/dock/update/result.json 2>/dev/null | tee -a $R; echo
d update 2>&1 | grep -i -E 'last update|version' | tee -a $R
bash /root/upg/snapshot.sh failed > /dev/null 2>&1
echo "variables same as after the first update: $(diff -q $EV/after/variables.txt $EV/failed/variables.txt >/dev/null && echo yes || echo no)" | tee -a $R
cat $EV/failed/auth.txt $EV/failed/routes.txt | tee -a $R
echo "integrity: $(cat $EV/failed/integrity.txt) migrations: $(cat $EV/failed/migrations.txt) secrets: $(cat $EV/failed/install.txt)" | tee -a $R

# Put the release source back.
cp $U/config.toml.orig /etc/dock/config.toml && systemctl restart dock && wait_api
