# Shared helpers for the end-to-end tests; source it from a test script.
#
# Each test runs the real guise binary against real mounts (NFS on macOS,
# FUSE on Linux) in a throwaway HOME, so it never touches your own guises,
# vaults, or Trash.
set -euo pipefail

TESTS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(dirname "$TESTS")

# The binary under test: $GUISE_BIN, or a fresh build.
if [[ -z "${GUISE_BIN:-}" ]]; then
	GUISE_BIN=$(mktemp -d)/guise
	(cd "$REPO" && go build -o "$GUISE_BIN" ./cmd/guise)
fi
G=$(cd "$(dirname "$GUISE_BIN")" && pwd)/$(basename "$GUISE_BIN")

HOME=$(mktemp -d)
export HOME XDG_CONFIG_HOME=$HOME/.config XDG_DATA_HOME=$HOME/.local/share
export GUISE_NO_NOTIFY=1 # no desktop notifications from test runs
cleanup() { "$G" stop >/dev/null 2>&1 || true; }
trap cleanup EXIT
cd "$HOME"

docx() { python3 "$TESTS/tools/docx.py" "$@"; }

pass=0
# check <description> <expected> <actual>
check() {
	if [[ "$2" == "$3" ]]; then
		pass=$((pass + 1))
		printf 'ok   %s\n' "$1"
	else
		printf 'FAIL %s\n  want: %q\n  got:  %q\n' "$1" "$2" "$3"
		exit 1
	fi
}

# settle waits for write-back (300 ms after the last write) to finish.
settle() { sleep 1; }

finish() { echo "$(basename "$0" .sh): $pass checks passed ($(uname -s))"; }

# vault_values sets name=value pairs in the default (plain) vault.
vault_values() {
	"$G" vault init --plain >/dev/null
	local kv
	for kv in "$@"; do
		echo "${kv#*=}" | "$G" vault set "${kv%%=*}"
	done
}
