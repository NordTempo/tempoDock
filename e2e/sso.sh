#!/usr/bin/env bash
# Single sign-on against a real Keycloak (ADR 0065, 0081): Keycloak runs
# in the slot as $SLOT-keycloak over HTTPS with a throwaway CA, which the
# control plane is made to trust. curl plays the browser: Dock's start,
# Keycloak's login form, the callback, with cookie jars.
#
# Covers the oidc kind and the keycloak preset, invite only, accepting an
# invite on the first sign-in, linking a second provider by email, an
# unverified email, a disabled account, removing a provider, a step-up
# making Keycloak ask for the password despite its own session, and
# signing out of Dock signing out of Keycloak too (RP-initiated logout).
#
#   e2e/vm/up.sh 0 && source /tmp/$E2E_SLOT/env && e2e/sso.sh
#
# Needs openssl and the Keycloak image (pulled on first use). KEEP=1
# leaves Keycloak and the providers in place.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

KC=$SLOT-keycloak
KC_IP=$SUBNET_PREFIX.40
KC_HOST=kc.$SLOT.test
KC_URL=https://$KC_HOST:8443
KC_IMAGE=${KC_IMAGE:-quay.io/keycloak/keycloak:26.4}
CP=$SLOT-cp
RUN=$(date +%s)
W=$(mktemp -d)

cleanup() {
	[[ ${KEEP:-} == 1 ]] && return
	dk sso remove "sso-oidc-$RUN" --yes >/dev/null 2>&1
	dk sso remove "sso-kc-$RUN" --yes >/dev/null 2>&1
	docker rm -fv "$KC" >/dev/null 2>&1
	rm -rf "$W"
}
trap cleanup EXIT

log "a CA and Keycloak's certificate"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$W/ca.key" -out "$W/ca.crt" -days 2 -subj "/CN=$SLOT e2e CA" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -keyout "$W/kc.key" -out "$W/kc.csr" -subj "/CN=$KC_HOST" 2>/dev/null
printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "$KC_HOST" >"$W/kc.ext"
openssl x509 -req -in "$W/kc.csr" -CA "$W/ca.crt" -CAkey "$W/ca.key" -CAcreateserial -out "$W/kc.crt" -days 2 -extfile "$W/kc.ext" 2>/dev/null
chmod 644 "$W/kc.key" "$W/kc.crt"

log "Keycloak at $KC_URL ($KC_IP)"
docker rm -fv "$KC" >/dev/null 2>&1
docker run -d --name "$KC" --network "$SLOT-net" --ip "$KC_IP" --memory 1g \
	-v "$W/kc.crt:/etc/kc/tls.crt:ro" -v "$W/kc.key:/etc/kc/tls.key:ro" \
	-e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
	"$KC_IMAGE" start-dev --https-certificate-file=/etc/kc/tls.crt --https-certificate-key-file=/etc/kc/tls.key \
	--hostname="$KC_URL" --http-enabled=true >/dev/null
# C is curl as the browser: it trusts the CA and finds Keycloak by name.
C() { curl -sS --cacert "$W/ca.crt" --resolve "$KC_HOST:8443:$KC_IP" "$@"; }
wait_for 180 C -fo /dev/null "$KC_URL/realms/master/.well-known/openid-configuration" 2>/dev/null || { fail "Keycloak didn't start"; exit 1; }

log "the control plane trusts the CA and resolves $KC_HOST"
docker cp "$W/ca.crt" "$CP:/usr/local/share/ca-certificates/$SLOT-e2e-ca.crt"
# /etc/hosts is a bind mount: rewrite it in place, sed -i can't rename over it.
docker exec "$CP" sh -c "update-ca-certificates >/dev/null 2>&1; grep -v ' $KC_HOST\$' /etc/hosts >/tmp/hosts; echo '$KC_IP $KC_HOST' >>/tmp/hosts
	cat /tmp/hosts >/etc/hosts; systemctl restart dock"
wait_for 60 curl -fs -o /dev/null "$API/health"

log "a realm with Dock's client and people"
kc() { docker exec "$KC" /opt/keycloak/bin/kcadm.sh "$@"; }
kc config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null
kc create realms -s realm=dock -s enabled=true >/dev/null
kc create clients -r dock -s clientId=dock -s publicClient=false -s secret=dock-secret -s standardFlowEnabled=true \
	-s "redirectUris=[\"${DOCK_URL%/}/api/v1/auth/sso/callback\"]" \
	-s "attributes.\"post.logout.redirect.uris\"=${DOCK_URL%/}/login" >/dev/null
person() { # person NAME VERIFIED: NAME-$RUN@example.com, password pw-NAME
	kc create users -r dock -s "username=$1" -s "email=$1-$RUN@example.com" -s "emailVerified=$2" -s enabled=true -s firstName="$1" -s lastName=Test >/dev/null
	kc set-password -r dock --username "$1" --new-password "pw-$1" >/dev/null
}
person ann true
person ben true
person cat false

# sso_signin PROVIDER USER JAR [CONFIRM] signs in through Keycloak as USER
# in cookie jar JAR, posting the password if Keycloak shows its form. It
# sets LANDED (where Dock sent the browser) and FORM (yes when Keycloak
# asked for the password).
sso_signin() {
	local provider=$1 user=$2 jar=$3 confirm=${4:-false} url hop action
	FORM=no LANDED=
	url=$(C -b "$jar" -c "$jar" -H 'Content-Type: application/json' \
		--data "{\"provider\":\"$provider\",\"confirm\":$confirm}" "$API/auth/sso/start" | jq -r .url)
	[[ $url == "$KC_URL"/* ]] || return 1
	hop=$(C -b "$jar" -c "$jar" -o "$jar.html" -w '%{redirect_url}' "$url")
	if [[ -z $hop ]]; then
		action=$(grep -o 'id="kc-form-login"[^>]*action="[^"]*"' "$jar.html" | sed 's/.*action="//; s/"$//; s/&amp;/\&/g')
		[[ -n $action ]] || return 1
		FORM=yes
		hop=$(C -b "$jar" -c "$jar" -o /dev/null -w '%{redirect_url}' --data-urlencode "username=$user" \
			--data-urlencode "password=pw-$user" "$action")
	fi
	[[ $hop == "${DOCK_URL%/}"/* ]] || return 1
	LANDED=$(C -b "$jar" -c "$jar" -o /dev/null -w '%{redirect_url}' "$hop")
	LANDED=${LANDED//+/ }
	LANDED=$(printf '%b' "${LANDED//%/\\x}")
}
# who JAR is the email the session in JAR signs in as, empty for none.
who() { C -b "$1" "$API/me" | jq -r '.email // empty'; }

log "the oidc kind and the keycloak preset, over HTTPS"
check "an oidc provider reads Keycloak's discovery through the CA" \
	dk sso add "sso-oidc-$RUN" --kind oidc --issuer "$KC_URL/realms/dock" --label "Corp ID" --client-id dock --client-secret-stdin <<<dock-secret
check "the keycloak preset builds the realm's issuer" \
	dk sso add "sso-kc-$RUN" --kind keycloak --domain "$KC_HOST:8443" --realm dock --client-id dock --client-secret-stdin <<<dock-secret

A=$W/ann
sso_signin "sso-oidc-$RUN" ann "$A"
check "invite only: no account and no invite lands on /invite-only" grep -q '/invite-only?' <<<"$LANDED"
api POST /org/invites "{\"email\":\"ann-$RUN@example.com\",\"role\":\"deploy\"}" >/dev/null
sso_signin "sso-oidc-$RUN" ann "$A"
check "with an invite: signed in on Keycloak's session, without the form again" test "$(who "$A")/$FORM" = "ann-$RUN@example.com/no"
check "the invite was accepted on the first sign-in" test "$(C -b "$A" "$API/me" | jq -r .scope)" = deploy
sso_signin "sso-kc-$RUN" ann "$W/ann2"
check "the keycloak preset links the same account by its verified email" test "$(who "$W/ann2")" = "ann-$RUN@example.com"

sso_signin "sso-kc-$RUN" cat "$W/cat"
check "an unverified email is refused with the reason" grep -q "hasn't verified cat-$RUN@example.com" <<<"$LANDED"

log "step-ups"
sso_signin "sso-kc-$RUN" ann "$A"
check "signing in again reuses Keycloak's session" test "$FORM" = no
sso_signin "sso-kc-$RUN" ann "$A" true
check "a step-up makes Keycloak ask for the password despite its session" test "$FORM/$(who "$A")" = "yes/ann-$RUN@example.com"

log "signing out and cutting people off"
end=$(C -b "$A" -c "$A" -X POST "$API/auth/logout" | jq -r '.end_session_url // empty')
check "logging out ends the Dock session" test -z "$(who "$A")"
check "logging out answers with Keycloak's end-session URL and the ID token" grep -q "^$KC_URL/realms/dock/.*id_token_hint=" <<<"$end"
back=$(C -b "$A" -c "$A" -o /dev/null -w '%{http_code} %{redirect_url}' "$end")
check "Keycloak signs out and sends the browser back to Dock's sign-in page" test "$back" = "302 ${DOCK_URL%/}/login"
sso_signin "sso-kc-$RUN" ann "$A"
check "after logout, Continue with Keycloak asks for the password again" test "$FORM/$(who "$A")" = "yes/ann-$RUN@example.com"
C -b "$A" -c "$A" -o /dev/null -X POST "$API/auth/logout"
api POST /org/invites "{\"email\":\"ben-$RUN@example.com\",\"role\":\"read\"}" >/dev/null
B=$W/ben
sso_signin "sso-kc-$RUN" ben "$B"
ben=$(api GET /users | jq -r --arg e "ben-$RUN@example.com" '.[] | select(.email == $e) | .id')
ui_login
ui_api POST "/users/$ben/disable" >/dev/null
check "disabling the account ends its session" test -z "$(who "$B")"
sso_signin "sso-kc-$RUN" ben "$B"
check "a disabled account can't sign in through Keycloak" grep -q "account is disabled" <<<"$LANDED"
ui_api POST "/users/$ben/enable" >/dev/null
sso_signin "sso-oidc-$RUN" ann "$A"
dk sso remove "sso-oidc-$RUN" --yes >/dev/null
check "removing a provider ends the sessions that came through it" test -z "$(who "$A")"

[[ ${FAILURES:-0} == 0 ]] || { echo "$FAILURES failed" >&2; exit 1; }
