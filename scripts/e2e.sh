#!/usr/bin/env bash
# End-to-end test: real vault, real guises, real mount (NFS on macOS, FUSE on
# Linux), edited with ordinary tools. Runs in a throwaway HOME so it never
# touches your own guises.
#
#   scripts/e2e.sh [path/to/guise]     (default: builds ./cmd/guise)
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
G=${1:-}
if [[ -z "$G" ]]; then
	G=$(mktemp -d)/guise
	(cd "$repo" && go build -o "$G" ./cmd/guise)
fi
G=$(cd "$(dirname "$G")" && pwd)/$(basename "$G")

export HOME=$(mktemp -d)
export XDG_CONFIG_HOME=$HOME/.config XDG_DATA_HOME=$HOME/.local/share
cleanup() { "$G" stop >/dev/null 2>&1 || true; }
trap cleanup EXIT
cd "$HOME"

pass=0
check() { # check <description> <expected> <actual>
	if [[ "$2" == "$3" ]]; then
		pass=$((pass + 1))
		printf 'ok   %s\n' "$1"
	else
		printf 'FAIL %s\n  want: %q\n  got:  %q\n' "$1" "$2" "$3"
		exit 1
	fi
}
settle() { sleep 1; } # write-back happens 300 ms after the last write

mkdir -p docs/job/notes ai
printf '# Matthew Wiltzius\nAustin · 512-555-0199\n' >docs/resume.md
printf 'Dear team, I am Matthew from Austin.\n' >docs/job/cover.md
printf 'call Wiltzius\n' >docs/job/notes/todo.txt
printf '\x89PNG\x00binary' >docs/job/photo.png
ln -s /etc/hosts docs/job/escape

"$G" vault init --plain >/dev/null
for kv in firstname=Matthew lastname=Wiltzius city=Austin; do
	echo "${kv#*=}" | "$G" vault set "${kv%%=*}"
done

# No start/mount command: `guise new` serves the guise immediately.
"$G" new ~/docs/resume.md ~/ai/resume.md </dev/null >/dev/null
"$G" new ~/docs/job ~/ai/job --expose city </dev/null >/dev/null

check "file guise hides values" \
	$'# {{pi.firstname}} {{pi.lastname}}\n{{pi.city}} · {{pi.unreviewed.phone.1}}' "$(cat ai/resume.md)"
check "directory guise honours expose" \
	"Dear team, I am {{pi.firstname}} from Austin." "$(cat ai/job/cover.md)"
check "nested files are guised" "call {{pi.lastname}}" "$(cat ai/job/notes/todo.txt)"
check "binary files and symlinks are hidden" "cover.md notes" "$(ls ai/job | tr '\n' ' ' | sed 's/ $//')"
check "no real values under ai/" "" "$(grep -rl -e Wiltzius -e 512-555-0199 -e Matthew ai/ || true)"

title="# Matthew Wiltzius" # expected first line of the target, as edits land
echo 'Signed, {{pi.firstname}} {{pi.lastname:upper}}' >>ai/resume.md
settle
check "append reaches the target" "Signed, Matthew WILTZIUS" "$(tail -1 docs/resume.md)"

python3 - <<'PY'
import os, tempfile
d = os.path.expanduser("~/ai/job/notes")
fd, tmp = tempfile.mkstemp(dir=d)
os.write(fd, b"From {{pi.firstname}} in {{pi.city}}\n")
os.close(fd)
os.replace(tmp, os.path.join(d, "new.md"))
PY
settle
check "atomic save creates the file with values" "From Matthew in Austin" "$(cat docs/job/notes/new.md)"

if command -v vim >/dev/null; then
	vim -Es -u NONE -c 'set backup writebackup' -c '1s/^# /# CV: /' -c wq ai/resume.md
	settle
	title="# CV: Matthew Wiltzius"
	check "vim backup-and-rename save" "$title" "$(head -1 docs/resume.md)"
fi

if sed --version >/dev/null 2>&1; then # GNU sed replaces symlinks with -i
	sed -i '1s/$/ (updated)/' ai/resume.md
	sleep 3
	title="$title (updated)"
	check "replaced symlink is restored" "yes" "$([[ -L ai/resume.md ]] && echo yes || echo no)"
	check "...and its edit applied" "$title" "$(head -1 docs/resume.md)"
fi

echo scrap >ai/job/scrap.md
rm ai/job/scrap.md
check "delete moves the target to the Trash" "no" "$([[ -e docs/job/scrap.md ]] && echo yes || echo no)"
check "no editor or OS junk in targets" "" \
	"$(find docs -name '._*' -o -name '*~' -o -name '.*.swp' -o -name '.guise-tmp*' -o -name 'tmp*' | sort)"

"$G" review promote unreviewed.phone.1 >/dev/null
sleep 2
check "review while serving" "{{pi.city}} · {{pi.phone}}" "$(sed -n 2p ai/resume.md)"

"$G" stop >/dev/null
check "stop makes guises unavailable" "no" "$([[ -e ai/resume.md ]] && echo yes || echo no)"
check "targets survive" "$title" "$(head -1 docs/resume.md)"

echo "e2e: $pass checks passed ($(uname -s))"
