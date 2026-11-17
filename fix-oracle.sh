#!/usr/bin/env bash
#
# fix-oracle.sh: get a validator's oracle attesting bridge transfers again
# after the Steem RPC failures of v0.0.8 ("invalid character '<'",
# "history: Internal Error", "Bad Cast", "oracle exited … ORACLE_START_BLOCK").
#
# What it does, in order:
#   1. Finds which oracle client this validator runs (go, python or js) from
#      the steemvm-oracle-* container (override: --client go|python|js).
#   2. Tests every known public Steem node FROM THIS SERVER (your IP may be
#      blocked by some) and rewrites ORACLE_STEEM_RPCS in oracle/.env to all
#      that answer: nodes that serve account history first (the svm.bank
#      signer reads it), your own non-public nodes before all of them.
#      ORACLE_STEEM_RPC is replaced only if it doesn't answer from here.
#      oracle/.env is backed up first; no other line changes, and no secret is
#      ever printed.
#   3. Pulls the fixed image (same tag) and recreates only the oracle
#      container (the node is not touched). The fixed oracle spreads its reads
#      round-robin over every node in the list, skips and benches a node that
#      fails, and rebuilds svm.bank's history from blocks when no node serves
#      it.
#   4. Watches the oracle's log for a minute and reports.
#
# The oracle keeps its Steem scan cursor in its data volume, so it resumes
# where it stopped and attests the deposits and payouts it missed.
#
# Usage:  ./fix-oracle.sh [--client go|python|js] [--dry-run] [--no-pull]
#   --dry-run   test the nodes and show the .env change; change nothing
#   --no-pull   don't pull the image (use the one already on this server)

set -euo pipefail
cd "$(dirname "$0")"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

CLIENT="" DRY_RUN=0 PULL=1
while [ $# -gt 0 ]; do
  case "$1" in
    --client) CLIENT="${2:-}"; shift 2 ;;
    --client=*) CLIENT="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --no-pull) PULL=0; shift ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

# Public Steem API nodes (https://steemitwallet.com/~witnesses node monitor).
# Those serving account history come first; the rest still serve blocks,
# prices and broadcasts, and the oracle skips them for history.
PUBLIC_NODES="https://api.steemit.com https://api.steemitdev.com https://api.steem.fans https://api.steememory.com https://api.moecki.online https://api.campingclub.me https://api.pennsif.net https://steemapi.boylikegirl.club https://api.justyy.com https://api2.justyy.com https://api3.justyy.com https://steem.senior.workers.dev https://api.symbionts.io https://api.steemyy.com https://steemd.steemworld.org"

command -v docker >/dev/null || die "docker is not installed."
command -v curl >/dev/null || die "curl is not installed (apt-get install -y curl)."
if docker compose version >/dev/null 2>&1; then COMPOSE="docker compose"
elif command -v docker-compose >/dev/null; then COMPOSE="docker-compose"
else die "docker compose is not installed."; fi
[ -f docker-compose.yml ] || die "run this from the SteemVirtualMachine-EVM checkout (docker-compose.yml not found)."

ENV_FILE="oracle/.env"
[ -f "$ENV_FILE" ] || die "$ENV_FILE not found — this server has no oracle configured."

# ── 1. which client ─────────────────────────────────────────────────────────
if [ -z "$CLIENT" ]; then
  found="$(docker ps -a --format '{{.Names}}' | sed -nE 's/^steemvm-oracle-(go|python|js)$/\1/p' | sort -u)"
  running="$(docker ps --format '{{.Names}}' | sed -nE 's/^steemvm-oracle-(go|python|js)$/\1/p' | sort -u)"
  if [ "$(printf '%s\n' "$running" | grep -c .)" = "1" ]; then CLIENT="$running"
  elif [ "$(printf '%s\n' "$found" | grep -c .)" = "1" ]; then CLIENT="$found"
  elif [ -z "$found" ]; then die "no steemvm-oracle-go/python/js container on this server. Start one, or pass --client go|python|js."
  else die "more than one oracle container exists ($(echo $found)) — a validator must run exactly one. Remove the extra one (docker rm -f steemvm-oracle-<client>) or pass --client."
  fi
fi
case "$CLIENT" in go|python|js) ;; *) die "--client must be go, python or js";; esac
CONTAINER="steemvm-oracle-$CLIENT"
SERVICE="oracle-$CLIENT"
state="$(docker inspect -f '{{.State.Status}}{{if .State.Restarting}} (restarting){{end}}' "$CONTAINER" 2>/dev/null)" || state="not created"
ok "oracle client: $CLIENT (container $CONTAINER: $state)"

# ── 2. Steem nodes ─────────────────────────────────────────────────────────
# A line is KEY=value, also with leading spaces, `export `, or spaces around =.
env_line() { printf '^[[:space:]]*(export[[:space:]]+)?%s[[:space:]]*=' "$1"; }
env_get() {
  sed -nE "s/$(env_line "$1")[[:space:]]*//p" "$ENV_FILE" | tail -1 | sed -E 's/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//; s/[[:space:]]+$//'
}
rpc() { # rpc <url> <method> <params-json>
  curl -s -m 12 -X POST -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$2\",\"params\":$3}" "$1" 2>/dev/null || true
}
answers()     { rpc "$1" condenser_api.get_dynamic_global_properties '[]' | grep -q '"head_block_number":[0-9]'; }
has_history() { rpc "$1" condenser_api.get_account_history '["svm.bank",-1,1]' | grep -q '"result":\[\['; }
is_public()   { case " $PUBLIC_NODES " in *" $1 "*) return 0 ;; esac; return 1; }

CUR_RPC="$(env_get ORACLE_STEEM_RPC | sed -E 's#/+$##')"
CUR_RPCS="$(env_get ORACLE_STEEM_RPCS)"
candidates=""
for n in $CUR_RPC $(printf '%s' "$CUR_RPCS" | tr ',' ' ') $PUBLIC_NODES; do
  n="${n%/}"
  [ -n "$n" ] || continue
  case " $candidates " in *" $n "*) continue ;; esac
  candidates="$candidates $n"
done

log "Testing $(echo $candidates | wc -w) Steem nodes from this server…"
own="" hist="" nohist="" dead=""
for n in $candidates; do
  if ! answers "$n"; then
    if is_public "$n"; then
      printf '   %-40s [1;31mno answer from this server (left out)[0m
' "$n"
    else
      # Your own node may be reachable only from the oracle's container (a
      # compose service, a private address): kept, last.
      dead="$dead $n"
      printf '   %-40s [1;33mno answer from here; your own node, kept last[0m
' "$n"
    fi
    continue
  fi
  if has_history "$n"; then h=1; label="ok, serves account history"; else h=0; label="ok, no account history"; fi
  printf '   %-40s %s\n' "$n" "$label"
  if ! is_public "$n"; then own="$own $n"
  elif [ "$h" = 1 ]; then hist="$hist $n"
  else nohist="$nohist $n"; fi
done
working="$(echo $own $hist $nohist)"
all_nodes="$(echo $working $dead)"
[ -n "$working" ] || die "no Steem node answers from this server — its IP is blocked everywhere or it has no internet. Run your own Steem node (or route through another server) and put it in ORACLE_STEEM_RPC."
[ -n "$(echo $own $hist)" ] || warn "no working node here serves account history: the svm.bank signer will rebuild it from blocks (slower start)."
NEW_RPCS="$(echo $all_nodes | tr ' ' ',')"

in_list() { case " $2 " in *" $1 "*) return 0 ;; esac; return 1; }
NEW_RPC="$CUR_RPC"
if [ -z "$CUR_RPC" ] || ! in_list "$CUR_RPC" "$working"; then
  NEW_RPC="$(echo $own $hist $nohist | awk '{print $1}')"
  warn "ORACLE_STEEM_RPC=${CUR_RPC:-<unset>} doesn't answer from this server — switching it to $NEW_RPC."
fi
ok "$(echo $working | wc -w) of $(echo $candidates | wc -w) nodes answer; the oracle will round-robin over all of them."

# ── rewrite oracle/.env (only these two keys) ───────────────────────────────
set_key() { # set_key <file> <key> <value>: replace every KEY= line, or append
  local f="$1" k="$2" v="$3" tmp
  tmp="$(mktemp "$f.XXXXXX")"
  awk -v k="$k" -v v="$v" -v re="$(env_line "$k")" '
    $0 ~ re { if (!done) { print k "=" v; done = 1 } ; next }
    { print }
    END { if (!done) print k "=" v }' "$f" > "$tmp"
  chmod --reference="$f" "$tmp" 2>/dev/null || chmod 600 "$tmp"
  mv "$tmp" "$f"
}
changed=0
[ "$NEW_RPC" = "$CUR_RPC" ] || changed=1
[ "$NEW_RPCS" = "$CUR_RPCS" ] || changed=1
if [ "$changed" = 0 ]; then
  ok "$ENV_FILE already has these nodes."
elif [ "$DRY_RUN" = 1 ]; then
  log "Dry run — $ENV_FILE would change:"
  [ "$NEW_RPC" = "$CUR_RPC" ]  || echo "   ORACLE_STEEM_RPC:  ${CUR_RPC:-<unset>}  ->  $NEW_RPC"
  [ "$NEW_RPCS" = "$CUR_RPCS" ] || echo "   ORACLE_STEEM_RPCS: ${CUR_RPCS:-<unset>}  ->  $NEW_RPCS"
else
  backup="$ENV_FILE.bak.$(date +%Y%m%d-%H%M%S)"
  cp -p "$ENV_FILE" "$backup"
  chmod 600 "$backup"
  set_key "$ENV_FILE" ORACLE_STEEM_RPC "$NEW_RPC"
  set_key "$ENV_FILE" ORACLE_STEEM_RPCS "$NEW_RPCS"
  ok "updated $ENV_FILE (backup: $backup)"
  [ "$NEW_RPC" = "$CUR_RPC" ] || echo "   ORACLE_STEEM_RPC=$NEW_RPC"
  echo "   ORACLE_STEEM_RPCS=$NEW_RPCS"
fi
[ "$DRY_RUN" = 1 ] && { log "Dry run: nothing pulled or restarted."; exit 0; }

# ── 3. pull the fixed image and recreate only the oracle ────────────────────
IMAGE="$($COMPOSE --profile "$CLIENT" config 2>/dev/null | awk -v s="  $SERVICE:" '$0==s{f=1;next} f&&/^  [^ ]/{f=0} f&&$1=="image:"{print $2; exit}')"
IMAGE="${IMAGE:-steemblazer/steemvm-oracle-$CLIENT:v0.0.8}"
before="$(docker image inspect -f '{{.Id}}' "$IMAGE" 2>/dev/null || true)"
if [ "$PULL" = 1 ]; then
  log "Pulling $IMAGE…"
  docker pull -q "$IMAGE" >/dev/null || die "could not pull $IMAGE (no internet, or Docker Hub rate limit) — retry, or use --no-pull."
  after="$(docker image inspect -f '{{.Id}}' "$IMAGE")"
  if [ "$before" = "$after" ]; then ok "$IMAGE is already the newest build."; else ok "pulled the new $IMAGE build."; fi
fi

log "Recreating $CONTAINER (the node keeps running)…"
if ! $COMPOSE --profile "$CLIENT" up -d --no-deps --force-recreate "$SERVICE" >/dev/null 2>&1; then
  # A container created from another checkout/project keeps the fixed name.
  warn "compose could not recreate $CONTAINER in place — removing the old container (its data volume is kept)."
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  $COMPOSE --profile "$CLIENT" up -d --no-deps "$SERVICE" >/dev/null || die "could not start $SERVICE: run '$COMPOSE --profile $CLIENT up -d $SERVICE' to see why."
fi
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ok "$CONTAINER restarted."

# ── 4. watch it for up to a minute ──────────────────────────────────────────
log "Watching the oracle log for up to 60s…"
verdict=""
for _ in $(seq 1 12); do
  sleep 5
  logs="$(docker logs --since "$started" "$CONTAINER" 2>&1 || true)"
  if printf '%s' "$logs" | grep -qiE 'oracle exited|refusing to start|Traceback|panic:'; then verdict=bad; break; fi
  if printf '%s' "$logs" | grep -qiE 'heartbeat|attest|scanned|steem oracle started|oracle will start'; then verdict=good; fi
done
printf '%s\n' "$logs" | grep -viE 'debug' | tail -15 | sed 's/^/   /'
st="$(docker inspect -f '{{.State.Status}}{{if .State.Restarting}} (restarting){{end}}' "$CONTAINER" 2>/dev/null)" || st=gone
case "$verdict:$st" in
  bad:*|*:*restarting*|*:exited|*:gone)
    die "the oracle is not healthy ($st). Check: docker logs --tail 100 $CONTAINER" ;;
  good:running)
    ok "the oracle is running and scanning Steem. It resumes from its saved cursor and attests the transfers it missed." ;;
  *)
    ok "the oracle is running ($st). Follow it with: docker logs -f --since 5m $CONTAINER" ;;
esac
