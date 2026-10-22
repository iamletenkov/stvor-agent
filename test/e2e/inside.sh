#!/bin/bash
# Scenario of the end-to-end test (plan Task 20, spec §15.3), run inside the
# sa-e2e-* container with systemd as PID 1 by test/e2e/run.sh. /it holds:
#   cream.deb, stvor-agent.deb  — the packages (cream by pin, agent from dist/)
#   ce.tar                      — git archive of cream-extensions at the pinned commit
#   fakeportal, teleport-fake   — the portal (robot API + /webapi/find) and the fake agent
# Environment: CATALOG_SHA256 (pin of the packed catalog), AGENT_VERSION,
# E2E_XFAIL (ids of checks failing because of known defects of the snapshot),
# TELEMETRY_EXPOSURE_MAX (test/e2e/telemetry-security.env: the highest allowed
# «Overall exposure level» of systemd-analyze security for the telemetry unit).
#
# One line per check: "ok", "FAIL", "xfail" (a known defect — not a failure),
# "XPASS" (a known defect gone — a failure until its id leaves E2E_XFAIL).
# Exit 0 — no FAIL and no XPASS.
set -uo pipefail

PASS=0 FAILS=0 XFAILS=0
TELEPORT_TARBALL=/it/teleport-v18.10.0-linux-amd64-bin.tar.gz
PORTAL=/var/lib/fakeportal
REQ=$PORTAL/requests.log
TREQ=$PORTAL/telemetry.log
TUNIT=stvor-agent-telemetry.service
TP=/var/lib/e2e-teleport
URL=https://portal.e2e/robot-api/v1/labels
# Test-only secrets of a throwaway container: the join token and the labels token.
JOIN_SECRET=e2e-join-token-0123456789
LABEL_TOKEN=e2eLabelToken_0123456789abcdefghijklmnopqrs

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
xfail_id() { [[ " ${E2E_XFAIL:-} " == *" $1 "* ]]; }
# check <id> <description> <command…>
check() {
	local id=$1 what=$2
	shift 2
	if "$@"; then
		if xfail_id "$id"; then
			echo "XPASS $id: $what — дефект исправлен? уберите $id из E2E_XFAIL (test/e2e/catalog-pin.env)"
			FAILS=$((FAILS + 1))
		else
			echo "ok    $id: $what"
			PASS=$((PASS + 1))
		fi
	elif xfail_id "$id"; then
		echo "xfail $id: $what (известный дефект снимка, см. test/e2e/catalog-pin.env)"
		XFAILS=$((XFAILS + 1))
	else
		echo "FAIL  $id: $what"
		FAILS=$((FAILS + 1))
	fi
}
summary() { echo "итог: ok $PASS, xfail $XFAILS, FAIL $FAILS"; }
fatal() {
	echo "FATAL $*"
	FAILS=$((FAILS + 1))
	summary
	exit 1
}
# wait_for <seconds> <command…> — poll once a second.
wait_for() {
	local n=$1 i
	shift
	for ((i = 0; i < n; i++)); do
		"$@" && return 0
		sleep 1
	done
	"$@"
}
eq() { [[ "$1" == "$2" ]]; }
inv() { systemctl show -p InvocationID --value "$1"; }
counter() { cat "$TP/$1" 2>/dev/null || echo 0; }
yaml_sum() { sha256sum /etc/teleport.yaml | cut -d' ' -f1; }
rev_of() { printf '%s\n%s' "$1" "$2" | sha256sum | cut -c1-16; }
managed() { cream run teleport managed-status | jq -c '[.managed, .pending, .joined, .active]'; }
# A tick of the service now (SIGHUP to the main process only: KillMode=mixed
# children must not get it).
tick() { kill -HUP "$(systemctl show -p MainPID --value stvor-agent.service)" 2>/dev/null; }
# poll_ticks <seconds> <command…> — like wait_for, but a tick every 10 s.
poll_ticks() {
	local n=$1 i
	shift
	for ((i = 0; i < n; i++)); do
		"$@" && return 0
		((i % 10 == 0)) && tick
		sleep 1
	done
	"$@"
}
# A labels request with this X-Applied-Revision and If-None-Match of the same
# revision, answered 304.
reported() { jq -e --arg r "$1" 'select(.x_applied_revision == $r and .if_none_match == "\"" + $r + "\"" and .status == 304)' "$REQ" >/dev/null 2>&1; }

# --- 0. cream, the portal and its CA --------------------------------------
log "cream по пину"
dpkg -i /it/cream.deb >/dev/null || fatal "dpkg -i cream"
cream version
cream system init >/dev/null || fatal "cream system init"
echo "127.0.0.1 portal.e2e" >>/etc/hosts

log "портал: /webapi/find, /robot-api/v1/labels и телеметрия"
install -d -m 0700 "$PORTAL"
printf '%s\n' "$LABEL_TOKEN" >"$PORTAL/token"
printf '{"env":"e2e","space":"sp-a"}\n' >"$PORTAL/labels.json"
install -m 0755 /it/fakeportal /usr/local/bin/fakeportal
systemd-run --unit=fakeportal --property=Type=exec /usr/local/bin/fakeportal -dir "$PORTAL" >/dev/null ||
	fatal "systemd-run fakeportal"
wait_for 20 test -s "$PORTAL/ca.pem" || fatal "портал не записал ca.pem"
cp "$PORTAL/ca.pem" /usr/local/share/ca-certificates/fakeportal.crt
update-ca-certificates >/dev/null 2>&1 || fatal "update-ca-certificates"
wait_for 20 curl -fsS -o /dev/null https://portal.e2e/webapi/find || fatal "портал не отвечает по https"

# --- 1. The catalog: snapshot of cream-extensions → cream catalog pack ------
log "каталог: снимок cream-extensions → cream catalog pack"
mkdir -p /it/x /it/cat/extensions
tar -xf /it/ce.tar -C /it/x || fatal "ce.tar"
mv /it/x/extensions/debian/teleport /it/x/extensions/debian/stvor-agent /it/cat/extensions/
printf 'schema: cream/v1\nname: sa-e2e\nversion: 1.0.0\nroots: [extensions]\n' >/it/cat/cream-catalog.yaml
SOURCE_DATE_EPOCH=0 cream catalog pack /it/cat --out /it/catalog.tgz >/dev/null || fatal "cream catalog pack"
got=$(sha256sum /it/catalog.tgz | cut -d' ' -f1)
[[ "$got" == "$CATALOG_SHA256" ]] || fatal "sha256 архива каталога $got ≠ пину $CATALOG_SHA256 — снимок не тот (CE_COMMIT) или пин устарел"
check catalog-pin "архив каталога совпал с пином" eq "$got" "$CATALOG_SHA256"
cream source add sa-e2e --archive file:///it/catalog.tgz --sha256 "$CATALOG_SHA256" --sync ||
	fatal "cream source add"

# --- 2. teleport with the fake agent ---------------------------------------
log "cream install teleport (поддельный teleport из тарбола)"
mkdir -p /it/tp/teleport
install -m 0755 /it/teleport-fake /it/tp/teleport/teleport
tar -czf "$TELEPORT_TARBALL" -C /it/tp teleport
tsha=$(sha256sum "$TELEPORT_TARBALL" | cut -d' ' -f1)
cream install teleport "TELEPORT_TARBALL=$TELEPORT_TARBALL" "TELEPORT_SHA256=$tsha" || fatal "cream install teleport"
check tp-binary "teleport version — 18.10.0" bash -c '/usr/local/bin/teleport version | grep -q v18.10.0'

# --- 3. join: the secret on stdin (РТ1) -------------------------------------
# An app of the operator: the node and the app must move together (ADR-0013).
cream set teleport TELEPORT_APPS=web=8080 || fatal "cream set teleport TELEPORT_APPS"
log "cream run teleport join (секрет — stdin)"
printf '%s' "$JOIN_SECRET" | cream run teleport join TELEPORT_PROXY=portal.e2e TELEPORT_REGISTRATION_SECRET=- \
	TELEPORT_ENV=e2e TELEPORT_NODENAME=e2e-node TELEPORT_LABELS=space=sp-a >/it/join.json
rc=$?
cat /it/join.json
check join-code "join — код 0" eq "$rc" 0
check join-result "join — registered" eq "$(jq -r .result /it/join.json)" registered
check join-active "teleport.service активен" systemctl is-active --quiet teleport.service
check join-secret-domain "секрет не попал в домен teleport" bash -c "! grep -rqF -- '$JOIN_SECRET' /etc/cream"
check join-scrub "секрет первого входа стёрт (teleport-token.service)" \
	wait_for 30 bash -c '! test -e /var/lib/cream/ext/teleport/registration-secret'
check join-yaml "teleport.yaml: space=sp-a у узла и приложения" \
	eq "$(grep -c '"space": "sp-a"' /etc/teleport.yaml)" 2

# --- 4. The wrapper stvor-agent: package from dist/, drop-in, reload --------
log "cream install stvor-agent (пакет из dist/: STVOR_AGENT_DEB + STVOR_AGENT_SHA256)"
inv0=$(inv teleport.service)
hups0=$(counter hups)
asha=$(sha256sum /it/stvor-agent.deb | cut -d' ' -f1)
cream install stvor-agent "STVOR_AGENT_VERSION=$AGENT_VERSION" "STVOR_AGENT_SHA256=$asha" \
	STVOR_AGENT_DEB=/it/stvor-agent.deb || fatal "cream install stvor-agent"
check sa-package "пакет stvor-agent $AGENT_VERSION стоит" \
	eq "$(dpkg-query -W -f='${Status} ${Version}' stvor-agent)" "install ok installed $AGENT_VERSION"
check sa-dropin "drop-in command-labels.d/stvor_agent на месте" \
	grep -qx 'command=/usr/bin/stvor-agent version --label' /etc/cream/teleport/command-labels.d/stvor_agent
check sa-not-started "служба без токена не стартовала (ConditionPathExists)" \
	bash -c '! systemctl is-active --quiet stvor-agent.service'
check label-render "render добавил команду stvor_agent в teleport.yaml" \
	wait_for 30 grep -q '"/usr/bin/stvor-agent", "version", "--label"' /etc/teleport.yaml
check label-reload "teleport.service получил reload (HUP) с командой stvor_agent" \
	wait_for 15 bash -c "[ \"\$(cat $TP/hups 2>/dev/null || echo 0)\" -gt $hups0 ] && grep -q '\"stvor_agent\"' $TP/config.hup"
check label-no-restart "установка обёртки не перезапустила teleport (тот же InvocationID)" \
	eq "$(inv teleport.service)" "$inv0"
check label-value "stvor-agent version --label без токена — пустая строка" \
	eq "$(/usr/bin/stvor-agent version --label)" ""

# --- 5. The unit: systemd-analyze -------------------------------------------
log "systemd-analyze verify / security stvor-agent.service"
systemd-analyze verify /usr/lib/systemd/system/stvor-agent.service
check unit-verify "systemd-analyze verify stvor-agent.service — код 0" eq "$?" 0
systemd-analyze security --no-pager stvor-agent.service
check unit-security "systemd-analyze security stvor-agent.service — код 0" eq "$?" 0

# --- 6. enable: the token on stdin; the first tick — set-managed, no restart -
log "stvor-agent enable (токен — stdin)"
inv1=$(inv teleport.service)
starts1=$(counter starts)
yaml1=$(yaml_sum)
printf '%s' "$LABEL_TOKEN" | stvor-agent enable "URL=$URL"
check enable-code "enable — код 0" eq "$?" 0
check enable-active "stvor-agent.service активен" systemctl is-active --quiet stvor-agent.service
check enable-token-mode "файл токена 0600 root" eq "$(stat -c '%a %u' /etc/stvor-agent/token)" "600 0"
check enable-first-200 "первый запрос к порталу — 200 без If-None-Match" \
	eq "$(head -n1 "$REQ" | jq -c '[.status, .if_none_match]')" '[200,""]'
check first-managed "управляемые метки env=e2e space=sp-a, pending=false" \
	eq "$(managed)" '[{"env":"e2e","space":"sp-a"},false,true,true]'
check first-no-restart "первый set-managed без restart (тот же InvocationID)" eq "$(inv teleport.service)" "$inv1"
check first-no-start "агент Teleport не стартовал заново" eq "$(counter starts)" "$starts1"
check first-yaml "итоговый teleport.yaml не изменился" eq "$(yaml_sum)" "$yaml1"
check label-value-enabled "stvor-agent version --label — $AGENT_VERSION" \
	eq "$(/usr/bin/stvor-agent version --label)" "$AGENT_VERSION"
rev_a=$(rev_of e2e sp-a)
check applied-a "X-Applied-Revision $rev_a на следующем тике" poll_ticks 60 reported "$rev_a"

# --- 6a. Telemetry (Stvor plan Task 18): run fetches the token, the service --
# starts as stvor-telemetry and a batch reaches the portal.
log "телеметрия: токен от run → служба stvor-telemetry → пачка у портала"
# treq <jq filter> — a telemetry request matching the filter was logged.
treq() { jq -e -s "any(.[]; $1)" "$TREQ" >/dev/null 2>&1; }
check tel-token-request "run запросил токен телеметрии по токену меток (POST /telemetry-token → 200)" \
	wait_for 30 treq '.path == "/robot-api/v1/telemetry-token" and .method == "POST" and .status == 200'
token_mode() { [[ "$(stat -c '%a %U:%G' /etc/stvor-agent-telemetry/token 2>/dev/null)" == "640 root:stvor-telemetry" ]]; }
check tel-token-file "файл токена телеметрии 0640 root:stvor-telemetry" wait_for 10 token_mode
check tel-token-dir "каталог /etc/stvor-agent-telemetry 0750 root:stvor-telemetry" \
	eq "$(stat -c '%a %U:%G' /etc/stvor-agent-telemetry)" "750 root:stvor-telemetry"
check tel-token-content "в файле — адрес robot-api и выданный порталом токен" \
	bash -c "grep -qx 'url=https://portal.e2e/robot-api/v1' /etc/stvor-agent-telemetry/token &&
		grep -qx \"token=\$(cat $PORTAL/telemetry-token)\" /etc/stvor-agent-telemetry/token"
check tel-token-not-labels "токен телеметрии не равен токену меток" \
	bash -c "! grep -qF -- '$LABEL_TOKEN' /etc/stvor-agent-telemetry/token"
check tel-active "run запустил $TUNIT" wait_for 30 systemctl is-active --quiet "$TUNIT"
tpid=$(systemctl show -p MainPID --value "$TUNIT")
check tel-user "служба телеметрии работает от stvor-telemetry, не root" \
	eq "$(ps -o user= -p "$tpid" | tr -d ' ')" stvor-telemetry
check tel-no-caps "у процесса телеметрии нет capabilities (CapEff 0)" \
	bash -c "grep -qx 'CapEff:[[:space:]]*0000000000000000' /proc/$tpid/status"
check tel-cgroup-limits "MemoryMax=64M и CPUQuota=10% применены" \
	bash -c "[ \"\$(systemctl show -p MemoryMax --value $TUNIT)\" = 67108864 ] &&
		[ \"\$(systemctl show -p CPUQuotaPerSecUSec --value $TUNIT)\" = 100ms ]"
check tel-metrics "пачка метрик дошла до портала: POST /metrics, gzip, 200, схема 1, ряды есть" \
	wait_for 60 treq '.path == "/robot-api/v1/metrics" and .encoding == "gzip" and .status == 200 and .schema == 1 and .series > 0 and (.batch_id | length) > 0'
check tel-inventory "инвентарь: хэш → 409 → полное тело → 200" \
	wait_for 30 bash -c "jq -e -s '[.[] | select(.path == \"/robot-api/v1/inventory\")] | (any(.status == 409 and (.inventory_body | not)) and any(.status == 200 and .inventory_body))' $TREQ >/dev/null"
if [ -s /etc/machine-id ]; then
	mid=$(tr -d '[:space:]' </etc/machine-id | sha256sum | cut -d' ' -f1)
	check tel-machine-id "machine_id_hash пачки — sha256 /etc/machine-id" \
		treq ".path == \"/robot-api/v1/metrics\" and .machine_id_hash == \"$mid\""
fi
check tel-no-401 "портал ни разу не отверг токен телеметрии" bash -c "! jq -e -s 'any(.[]; .status == 401)' $TREQ >/dev/null"
check tel-status "status --json: служба active, токен ok, last_send есть" \
	wait_for 20 bash -c "stvor-agent status --json | jq -e '.telemetry.service == \"active\" and .telemetry.token == \"ok\" and .telemetry.last_send != null' >/dev/null"
check tel-token-not-in-journal "токены не попали в журнал" \
	bash -c "! journalctl --no-pager -o cat | grep -qF -e \"\$(cat $PORTAL/telemetry-token)\" -e '$LABEL_TOKEN'"
check tel-stop "SIGTERM: служба останавливается штатно, буфер на диске" \
	bash -c "systemctl stop $TUNIT && [ \"\$(systemctl show -p Result --value $TUNIT)\" = success ] && test -d /var/lib/stvor-agent-telemetry/buffer"
systemctl start "$TUNIT"

log "systemd-analyze verify / security $TUNIT"
systemd-analyze verify "/usr/lib/systemd/system/$TUNIT"
check tel-unit-verify "systemd-analyze verify $TUNIT — код 0" eq "$?" 0
systemd-analyze security --no-pager "$TUNIT" | tee /it/telemetry-security.txt
exposure=$(sed -n 's/.*Overall exposure level for [^:]*: \([0-9.]*\).*/\1/p' /it/telemetry-security.txt)
log "exposure $TUNIT: ${exposure:-?} (порог ${TELEMETRY_EXPOSURE_MAX:-?})"
check tel-unit-exposure "exposure $TUNIT ${exposure:-?} ≤ ${TELEMETRY_EXPOSURE_MAX:-?}" \
	awk -v got="${exposure:-}" -v max="${TELEMETRY_EXPOSURE_MAX:-}" 'BEGIN { exit !(got != "" && max != "" && got + 0 <= max + 0) }'

# --- 7. The portal moves the robot: restart confirmed, X-Applied-Revision ----
log "портал меняет space: sp-a → sp-b"
inv2=$(inv teleport.service)
starts2=$(counter starts)
printf '{"env":"e2e","space":"sp-b"}\n' >"$PORTAL/labels.json"
t0=$(date +%s)
moved() { [[ "$(managed)" == '[{"env":"e2e","space":"sp-b"},false,true,true]' && "$(inv teleport.service)" != "$inv2" ]]; }
check move-applied "управляемый space=sp-b, restart подтверждён, pending=false" poll_ticks 90 moved
took=$(($(date +%s) - t0))
check move-restart "teleport.service перезапущен (InvocationID сменился, новый старт агента)" \
	bash -c "[ \"\$(systemctl show -p InvocationID --value teleport.service)\" != '$inv2' ] && [ \"\$(cat $TP/starts)\" -gt $starts2 ]"
check move-yaml "teleport.yaml: space=sp-b у узла и приложения, sp-a нет" \
	bash -c "[ \"\$(grep -c '\"space\": \"sp-b\"' /etc/teleport.yaml)\" = 2 ] && ! grep -q 'sp-a' /etc/teleport.yaml"
check move-agent-config "агент Teleport стартовал с конфигом sp-b" grep -q '"space": "sp-b"' "$TP/config.start"
# set-managed holds lock/teleport while it restarts teleport.service; render of
# ExecStartPre is lock: none, so the restart does not wait for the lock (cream
# spec §7.3). A render under the lock would hang the restart past the 30 s
# confirmation of set-managed and come out as unconfirmed.
check move-no-deadlock "restart под блокировкой не ждал ExecStartPre render (${took} с < 60 с)" test "$took" -lt 60
check move-no-unconfirmed "в журнале stvor-agent нет unconfirmed и занятой блокировки" \
	bash -c "! journalctl -u stvor-agent.service --no-pager -o cat | grep -qE 'unconfirmed|не подтвердил|занята|lock_busy'"
rev_b=$(rev_of e2e sp-b)
check applied-b "X-Applied-Revision $rev_b на следующем тике" poll_ticks 60 reported "$rev_b"

# --- 8. What is left for the log -------------------------------------------
log "stvor-agent status --json"
stvor-agent status --json
log "cream run teleport status"
cream run teleport status
log "запросы к порталу"
cat "$REQ"
log "запросы телеметрии"
cat "$TREQ"

summary
[ "$FAILS" -eq 0 ]
