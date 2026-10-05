#!/usr/bin/env bash
# End-to-end: Word documents through a directory guise and a file guise.
source "$(dirname "$0")/lib.sh"

mkdir -p docs/job ai
vault_values firstname=Peter lastname=Parker alias=Spiderman

# Word splits text into runs freely; "|" puts each piece in its own run.
docx make docs/job/cover.docx "Dear team, I am Pe|ter Par|ker." "Also known as Spider|man. Call 212-555-0187."
docx make docs/resume.docx "Peter Parker" "Photographer, Daily Bugle"
"$G" new ~/docs/job ~/ai/job </dev/null >/dev/null
"$G" new ~/docs/resume.docx ~/ai/resume.docx </dev/null >/dev/null

check "document in a directory guise is shown" "cover.docx" "$(ls ai/job)"
check "values split across runs are hidden" \
	$'Dear team, I am {{pi.firstname}} {{pi.lastname}}.\nAlso known as {{pi.alias}}. Call {{pi.unreviewed.phone.1}}.' \
	"$(docx text ai/job/cover.docx)"
check "file guise of a document" $'{{pi.firstname}} {{pi.lastname}}\nPhotographer, Daily Bugle' \
	"$(docx text ai/resume.docx)"
check "no real values in guises" "" "$(cd ai && for f in job/cover.docx resume.docx; do docx text "$f"; done | grep -e Peter -e Parker -e 212-555 || true)"

if command -v textutil >/dev/null; then # macOS: a real document reader accepts the guise
	check "textutil reads the guise" "{{pi.firstname}} {{pi.lastname}}" \
		"$(textutil -convert txt -stdout ai/resume.docx | head -1)"
fi

# Save an edited document the way Word does: write a temp file next to it,
# then rename it over the original. Placeholders may be split across runs.
docx make "$HOME/edited.docx" "Dear {{pi.first|name}} {{pi.lastname:upper}}," "Thanks, {{pi.alias}}."
# (Written with cat, not cp: macOS's NFS client can fail to copy the
# com.apple.provenance attribute after the directory was listed, making cp
# report an error even though the contents copied fine.)
cat "$HOME/edited.docx" >"ai/job/~WRL0001.tmp"
mv -f "ai/job/~WRL0001.tmp" ai/job/cover.docx
settle
check "edited document reaches the target with values" $'Dear Peter PARKER,\nThanks, Spiderman.' \
	"$(docx text docs/job/cover.docx)"

# Save the way sandboxed Word does: through a staging folder macOS creates
# next to the document, moving the original in as a backup.
stage="ai/job/cover.docx.sb-d4785637-AgmyQ4"
mkdir -p "$stage/~WRL4041.sb-d4785637-ewngkf"
echo scratch >"$stage/~WRL4041.sb-d4785637-ewngkf/.~WRD3856"
docx make "$HOME/edited3.docx" "Sincerely, {{pi.firstname}}"
cat "$HOME/edited3.docx" >"$stage/.~WRD0000"
mv ai/job/cover.docx "$stage/~WRL4041.tmp"
mv "$stage/.~WRD0000" ai/job/cover.docx
rm -rf "$stage"
settle
check "sandboxed Word-style save reaches the target" "Sincerely, Peter" "$(docx text docs/job/cover.docx)"
check "staging folders stay out of the real folder" "cover.docx" "$(ls docs/job)"

# In-place save through a file guise.
docx make "$HOME/edited2.docx" "{{pi.lastname}}, {{pi.firstname}}" "Photographer"
cat "$HOME/edited2.docx" >ai/resume.docx
settle
check "in-place save through a file guise" $'Parker, Peter\nPhotographer' "$(docx text docs/resume.docx)"

# Word's lock file stays out of the real folder.
echo lock >"ai/job/~\$cover.docx"
check "Word lock file kept out of the target" "no" "$([[ -e "docs/job/~\$cover.docx" ]] && echo yes || echo no)"
rm "ai/job/~\$cover.docx"

# A document with a value somewhere guise does not transform is withheld.
docx make docs/job/addin.docx "harmless text"
docx add-part docs/job/addin.docx word/webextensions/taskpane.xml '<x note="Parker add-in"/>'
sleep 2
check "document with unhandled PI is hidden" "cover.docx" "$(ls ai/job)"

# A save cut short leaves the target alone.
before=$(docx text docs/resume.docx)
head -c 200 "$HOME/edited.docx" >ai/resume.docx
settle
check "truncated save does not touch the target" "$before" "$(docx text docs/resume.docx)"

finish
