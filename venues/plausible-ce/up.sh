#!/usr/bin/env bash
# A local Plausible Community Edition to run the plausible wrapper's
# conformance against, with no one's Plausible account (doc 35 §8: a venue
# the suite can prove things against).
#
#   ./venues/plausible-ce/up.sh          # start, seed, send traffic; prints how to run the suite
#   ./venues/plausible-ce/up.sh down     # stop and remove everything, data included
#
# Everything it generates -- the instance's secret, a user's password, a
# Stats API key -- is local to this machine, lives in venues/plausible-ce/.work/
# (git-ignored), and opens nothing but 127.0.0.1:8765. Safe to run again: an
# instance already seeded is reused as it is.
#
# CE 3.2.1 serves the v2 Stats API the wrapper uses (checked 2026-09-28). The
# known traffic -- 37 pageviews from 11 visitors -- is sent once, on the day
# the venue is first brought up, and never again: Plausible does not count a
# repeat of the same visitors' pageviews minutes later, so a venue topped up on
# every run would hold numbers that depend on how often it ran. The day is
# recorded, and the suite is pointed at it with -at.
set -euo pipefail

CE_TAG=v3.2.1
PORT=8765
SITE=conformance.test
PROJECT=mendel-plausible-ce
HERE=$(cd "$(dirname "$0")" && pwd)
WORK="$HERE/.work"
CE="$WORK/community-edition"

if [ "${1:-}" = down ]; then
    [ -d "$CE" ] && docker compose -p "$PROJECT" --project-directory "$CE" down -v
    rm -rf "$WORK"
    exit 0
fi

mkdir -p "$WORK"
if [ ! -d "$CE" ]; then
    git clone -q --depth 1 --branch "$CE_TAG" https://github.com/plausible/community-edition "$CE"
fi

# Generated once and kept, so a rerun reaches the same seeded instance.
umask 077
if [ ! -f "$WORK/venue.env" ]; then
    {
        echo "PLAUSIBLE_CE_SECRET_KEY_BASE=$(openssl rand -base64 64 | tr -d '\n')"
        echo "PLAUSIBLE_CE_PASSWORD=$(openssl rand -base64 30 | tr -dc 'A-Za-z0-9' | head -c 28)"
        echo "PLAUSIBLE_API_KEY=$(openssl rand -base64 72 | tr -dc 'A-Za-z0-9' | head -c 64)"
    } > "$WORK/venue.env"
fi
set -a; . "$WORK/venue.env"; set +a

cat > "$CE/.env" <<EOF
BASE_URL=http://localhost:$PORT
SECRET_KEY_BASE=$PLAUSIBLE_CE_SECRET_KEY_BASE
HTTP_PORT=80
ENABLE_EMAIL_VERIFICATION=false
EOF
cat > "$CE/compose.override.yml" <<EOF
services:
  plausible:
    ports:
      - 127.0.0.1:$PORT:80
EOF

echo "=== Starting Plausible CE $CE_TAG on http://localhost:$PORT ==="
docker compose -p "$PROJECT" --project-directory "$CE" up -d --wait >/dev/null
for _ in $(seq 1 60); do
    curl -fsS -o /dev/null "http://localhost:$PORT/api/health" 2>/dev/null && break
    sleep 2
done

# Seeded through the release's own modules: a user, their team, a site in
# UTC, and a Stats API key. Idempotent: an existing user is reused.
echo "=== Seeding $SITE and a Stats API key ==="
docker compose -p "$PROJECT" --project-directory "$CE" exec -T plausible /app/bin/plausible rpc "
pw = \"$PLAUSIBLE_CE_PASSWORD\"
user = Plausible.Repo.get_by(Plausible.Auth.User, email: \"conformance@plausible.test\") ||
  Plausible.Repo.insert!(Plausible.Auth.User.new(%{name: \"Conformance\", email: \"conformance@plausible.test\",
    password: pw, password_confirmation: pw}))
{:ok, team} = Plausible.Teams.get_or_create(user)
unless Plausible.Repo.get_by(Plausible.Site, domain: \"$SITE\") do
  {:ok, _} = Plausible.Sites.create(user, %{\"domain\" => \"$SITE\", \"timezone\" => \"Etc/UTC\"})
end
if Plausible.Auth.find_api_key(\"$PLAUSIBLE_API_KEY\") == {:error, :invalid_api_key} do
  {:ok, _} = Plausible.Auth.create_stats_api_key(user, team, \"conformance\", \"$PLAUSIBLE_API_KEY\")
end
# Ingestion accepts an event only for a site in its in-memory cache, which
# refreshes on a timer; an event for a site created seconds ago is answered
# 202 and dropped. Refresh it now rather than wait.
Plausible.Site.Cache.refresh_updated_recently()
" >/dev/null

# Known traffic, once: 37 pageviews from 11 visitors, a third of them on
# /pricing. Sent only once ingestion's cache holds the site: the refresh above
# is asynchronous, and events that arrive before it lands are dropped.
if [ ! -f "$WORK/traffic-day" ]; then
for _ in $(seq 1 30); do
    ready=$(docker compose -p "$PROJECT" --project-directory "$CE" exec -T plausible /app/bin/plausible rpc \
        "IO.puts(if Plausible.Site.Cache.get(\"$SITE\"), do: \"ready\", else: \"waiting\")" 2>/dev/null | tail -1)
    [ "$ready" = ready ] && break
    sleep 2
done
echo "=== Sending 37 pageviews from 11 visitors ==="
for i in $(seq 1 37); do
    ip="10.0.0.$(( i % 11 + 1 ))"
    page=/; [ $(( i % 3 )) = 0 ] && page=/pricing
    curl -fsS -o /dev/null -X POST "http://localhost:$PORT/api/event" \
        -H 'Content-Type: application/json' \
        -H "User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15 v$ip" \
        -H "X-Forwarded-For: $ip" \
        -d "{\"name\":\"pageview\",\"url\":\"http://$SITE$page\",\"domain\":\"$SITE\"}"
done
# Read back before saying ready: a 202 does not mean an event was counted.
query='{"site_id":"'$SITE'","metrics":["pageviews","visitors"],"date_range":"day"}'
for _ in $(seq 1 30); do
    got=$(curl -fsS -X POST "http://localhost:$PORT/api/v2/query" -H "Authorization: Bearer $PLAUSIBLE_API_KEY" \
        -H 'Content-Type: application/json' -d "$query" | sed -n 's/.*"metrics":\[\([0-9]*\),\([0-9]*\)\].*/\1 \2/p')
    [ "$got" = "37 11" ] && break
    sleep 2
done
if [ "$got" != "37 11" ]; then
    echo "Plausible counted pageviews and visitors \"$got\", not \"37 11\"; the venue is not what the suite expects." >&2
    echo "Remove it and start again: ./venues/plausible-ce/up.sh down && ./venues/plausible-ce/up.sh" >&2
    exit 1
fi
date -u +%Y-%m-%d > "$WORK/traffic-day"
fi
DAY=$(cat "$WORK/traffic-day")

cat <<EOF

Ready. From the repository root:

  go build -mod=vendor -o /tmp/plausible ./plausible
  go run -mod=vendor ./cmd/conformance -file plausible/wrapper.json -cmd /tmp/plausible \\
      -account $SITE -endpoint http://localhost:$PORT -at $(date -u -j -v+1d -f %Y-%m-%d "$DAY" +%Y-%m-%dT06:00:00Z 2>/dev/null || date -u -d "$DAY + 1 day" +%Y-%m-%dT06:00:00Z)

with the key in the environment: set -a; . venues/plausible-ce/.work/venue.env; set +a
The suite reads $DAY, the day the traffic was sent: expect pageviews 37, visitors 11, visits 11.
Stop and remove it: ./venues/plausible-ce/up.sh down
EOF
