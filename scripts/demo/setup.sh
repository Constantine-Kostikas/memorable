#!/bin/sh
# Builds an isolated memorable demo: its own Postgres container, config, and credentials,
# seeded with fictional memories for a payments service. Your real install is not touched.
#
#   scripts/demo/setup.sh [dir]     (default dir: /tmp/memorable-demo)
#
# Keep dir outside your home directory: Claude Code loads CLAUDE.md files from every
# parent of the project, so a demo under ~ would pick up your real ~/.claude/CLAUDE.md.
#   . <dir>/env.sh                  then run memorable, claude, or vhs from <dir>/home/code/payments-api
#
# Remove it afterward with: docker rm -f memorable-demo-pg && rm -rf <dir>
set -eu

root=$(cd "$(dirname "$0")" && pwd)
target=${1:-/tmp/memorable-demo}
dir=$(mkdir -p "$target" && cd "$target" && pwd)
port=5444
password=demo-admin

home="$dir/home"
project="$home/code/payments-api"
mkdir -p "$home/.config/memorable" "$project"
cat > "$dir/env.sh" <<ENV
export HOME="$home" XDG_CONFIG_HOME="$home/.config" XDG_DATA_HOME="$home/.local/share"
export MEMORABLE_KEYRING=off MEMORABLE_CONFIG="$home/.config/memorable/config.toml"
ENV
cat > "$home/.config/memorable/config.toml" <<CFG
[contexts.work]
paths = ["~/code"]

[database]
port = $port
container = "memorable-demo-pg"
CFG

git -C "$project" init -q
git -C "$project" remote add origin git@github.com:acme/payments-api.git 2>/dev/null || true

docker run -d --name memorable-demo-pg -p "127.0.0.1:$port:5432" \
  -e POSTGRES_USER=memorable_admin -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB=memorable \
  pgvector/pgvector:0.8.7-pg18-trixie >/dev/null
until docker exec memorable-demo-pg pg_isready -q -U memorable_admin -h 127.0.0.1; do sleep 1; done

. "$dir/env.sh"
cd "$project"
echo "Enter the demo admin password when asked: $password"
memorable migrate
memorable import "$root/seed.json"
memorable reembed
echo "Demo ready. Run: . $dir/env.sh && cd $project"
