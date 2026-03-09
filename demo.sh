#!/usr/bin/env bash
# demo.sh — run 8 simulated microservices through spex (~13 s total).
# One service (mailer) exits non-zero on purpose to show failure handling.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPEX="$SCRIPT_DIR/spex"

printf 'Building spex...\n' >&2
go build -o "$SPEX" "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Write the log-generator to a temp file so each runner can invoke it.
# ---------------------------------------------------------------------------
HELPER=$(mktemp /tmp/spex_demo_XXXXXX.sh)
trap 'rm -f "$HELPER"' EXIT

cat > "$HELPER" <<'GENEOF'
#!/usr/bin/env bash
# Args: <duration_seconds> [exit_code:-0]
duration=${1:?usage: <duration> [exit_code]}
exit_code=${2:-0}

levels=(INFO INFO INFO INFO DEBUG DEBUG WARN)
msgs=(
  "received request method=GET path=/api/v1/status"
  "db query table=events rows=RAND latency=RANDms"
  "cache HIT key=session:RAND"
  "cache MISS key=user:RAND — fetching from origin"
  "queue depth=RAND jobs pending"
  "goroutine pool size=RAND active=RAND"
  "checkpoint written offset=RAND"
  "flushed RAND dirty records to storage"
  "retrying upstream host=backend-RAND attempt=RAND/3"
  "health check: OK latency=RANDms"
  "token validated sub=usr_RAND ttl=RANDs"
  "batch commit txn=RAND rows=RAND affected"
  "conn pool active=RAND/20 idle=RAND waiting=RAND"
  "sweep complete: RAND stale entries evicted"
  "replication lag=RANDms behind replica=RAND"
  "WAL segment=RAND compacted freed=RANDkb"
)
n=${#msgs[@]}

start=$(date +%s)
seq=0
while true; do
  elapsed=$(( $(date +%s) - start ))
  (( elapsed >= duration )) && break

  lvl=${levels[$((RANDOM % ${#levels[@]}))]}
  msg="${msgs[$((RANDOM % n))]}"

  # Replace each RAND token with its own random value (different per token).
  while [[ $msg == *RAND* ]]; do
    msg="${msg/RAND/$((RANDOM % 900 + 10))}"
  done

  printf '[%s] %s seq=%04d\n' "$lvl" "$msg" "$seq"
  # Occasionally emit a trailing blank line to test blank-line suppression.
  (( RANDOM % 4 == 0 )) && printf '\n'
  seq=$(( seq + 1 ))
  sleep "0.$((RANDOM % 5 + 4))"   # 0.4 – 0.8 s between lines
done

if (( exit_code != 0 )); then
  printf '[ERROR] process terminated abnormally exit_code=%d\n' "$exit_code"
fi
exit "$exit_code"
GENEOF

chmod +x "$HELPER"

# ---------------------------------------------------------------------------
# 8 services, 4 parallel — approximate timeline:
#   t= 0  api-server auth-service worker-a worker-b  (4 running)
#   t= 4  worker-a done  → cache-loader starts
#   t= 5  worker-b done  → mailer starts
#   t= 7  auth-service + cache-loader done → notifier + migrator start
#   t=10  api-server done
#   t=12  mailer (FAIL) + migrator done
#   t=13  notifier done                              (≈ 13 s total)
# ---------------------------------------------------------------------------
"$SPEX" --name Processes --output errors --max-parallel 4 --tail 3 <<RUNNERS
api-server	bash "$HELPER" 10
auth-service	bash "$HELPER" 7
worker-a	bash "$HELPER" 4
worker-b	bash "$HELPER" 5
cache-loader	bash "$HELPER" 3
mailer	bash "$HELPER" 7 1
notifier	bash "$HELPER" 6
migrator	bash "$HELPER" 5
RUNNERS
