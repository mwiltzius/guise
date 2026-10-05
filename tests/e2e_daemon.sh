#!/usr/bin/env bash
# End-to-end: the background process. Auto-start, encrypted vaults,
# login-style start without passphrases, unlock, stop/start cycles, and
# staying responsive while a vault is decrypted.
source "$(dirname "$0")/lib.sh"

mkdir -p docs ai
printf 'Peter at the Daily Bugle\n' >docs/notes.md
printf 'secret\n' | "$G" vault init "$HOME/enc.age" >/dev/null
printf 'secret\nPeter\n' | "$G" vault set firstname --vault "$HOME/enc.age"

check "nothing running before the first guise" "Not running." "$("$G" status | cut -d' ' -f1-2)"
printf 'secret\n' | "$G" new ~/docs/notes.md ~/ai/notes.md --vault "$HOME/enc.age" >/dev/null
check "guise new serves immediately (no start command)" "{{pi.firstname}} at the Daily Bugle" "$(cat ai/notes.md)"

# While the encrypted vault is re-read after an edit (scrypt: ~1 s by
# design), reads through the mount must stay fast.
slowest=$(python3 - "$G" "$HOME/enc.age" <<'PY'
import subprocess, sys, threading, time, os
g, vault = sys.argv[1], sys.argv[2]
p = subprocess.Popen([g, "vault", "set", "employer", "--vault", vault], stdin=subprocess.PIPE,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
p.communicate(b"secret\nDaily Bugle\n")
worst, end = 0.0, time.time() + 4  # the background process reloads within ~1 s
while time.time() < end:
    t = time.time()
    open(os.path.expanduser("~/ai/notes.md")).read()
    worst = max(worst, time.time() - t)
    time.sleep(0.02)
print("fast" if worst < 1.0 else f"slow ({worst:.2f}s)")
PY
)
check "reads stay fast while the vault is decrypted" "fast" "$slowest"
check "vault edit shows up in the guise" "{{pi.firstname}} at the {{pi.employer}}" "$(cat ai/notes.md)"

for i in 1 2 3; do
	"$G" stop >/dev/null
	check "stop $i makes the guise unavailable" "no" "$([[ -e ai/notes.md ]] && echo yes || echo no)"
	# Start the way login does: no passphrases.
	"$G" _serve >>"$HOME/serve.log" 2>&1 &
	for _ in $(seq 50); do "$G" status >/dev/null 2>&1 && break; sleep 0.1; done
	check "start $i at login leaves the vault locked" "Locked vault: $HOME/enc.age" "$("$G" status | grep Locked)"
	printf 'secret\n' | "$G" unlock >/dev/null
	sleep 1
	check "unlock $i serves the guise" "{{pi.firstname}} at the {{pi.employer}}" "$(cat ai/notes.md)"
done

"$G" stop >/dev/null
check "no background process left" "0" "$(pgrep -f "$G _serve" | wc -l | tr -d ' ')"
check "target untouched throughout" "Peter at the Daily Bugle" "$(cat docs/notes.md)"

finish
