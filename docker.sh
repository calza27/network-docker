#!/usr/bin/env bash
die() { echo "${1:-urgh}" >&2; exit "${2:-1}"; }

usage() {
    cat <<EOF
Usage: ${0##*/} <up|down|pull> <container1> [container2 ...]
Commands:
  up    - start the specified containers
  down  - stop the specified containers
  pull  - perfornms a docker compose pull then restarts the specified containers
EOF
}

hash docker 2>/dev/null || die "docker not found"

if [[ $# -lt 2 ]]; then
    usage
    die "Not enough arguments"
fi

action="$1"
shift

containers=()
if [[ "$1" == "all" ]]; then
    containers=$(find . -maxdepth 2 -name "docker-compose.yml" -exec dirname {} \; | sed 's|^\./||')
else
    containers="$@"
fi

fails=()
case "$action" in
    up)
        for container in "$containers"; do
            if [[ -d "$container" ]]; then
                (cd "$container" && docker compose up -d) || fails+=("$container")
            else
                echo "Directory for container '$container' not found, skipping."
            fi
            echo ""
        done
        ;;
    down)
        for container in "$containers"; do
            if [[ -d "$container" ]]; then
                (cd "$container" && docker compose down) || fails+=("$container")
            else
                echo "Directory for container '$container' not found, skipping."
            fi
            echo ""
        done
        ;;
    pull)
        for container in "$containers"; do
            if [[ -d "$container" ]]; then
                (cd "$container" && docker compose pull && docker container up -d) || fails+=("$container")
            else
                echo "Directory for container '$container' not found, skipping."
            fi
            echo ""
    *)
        usage
        die "Unknown action: $action"
        ;;
esac

if [[ ${#fails[@]} -ne 0 ]]; then
    echo "The following containers failed to $action: ${fails[*]}"
    exit 1
fi