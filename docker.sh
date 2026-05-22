#!/usr/bin/env bash
die() { echo "${1:-urgh}" >&2; exit "${2:-1}"; }

usage() {
    cat <<EOF
Usage: ${0##*/} <up|down> <container1> [container2 ...]
Commands:
  up    - start the specified containers
  down  - stop the specified containers
EOF
}

hash docker 2>/dev/null || die "docker not found"

if [[ $# -lt 2 ]]; then
    usage
    die "Not enough arguments"
fi

action="$1"
shift

fails=()
case "$action" in
    up)
        for container in "$@"; do
            # cd into the container directory to run docker compose commands
            # assumes each container has its own directory with a docker-compose.yml file
            if [[ -d "$container" ]]; then
                (cd "$container" && docker compose up -d) || fails+=("$container")
            else
                echo "Directory for container '$container' not found, skipping."
            fi
            echo ""
        done
        ;;
    down)
        for container in "$@"; do
            if [[ -d "$container" ]]; then
                (cd "$container" && docker compose down) || fails+=("$container")
            else
                echo "Directory for container '$container' not found, skipping."
            fi
            echo ""
        done
        ;;
    *)
        usage
        die "Unknown action: $action"
        ;;
esac

if [[ ${#fails[@]} -ne 0 ]]; then
    echo "The following containers failed to $action: ${fails[*]}"
    exit 1
fi