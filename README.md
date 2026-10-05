# guise

**The same file, in a different guise.**

`guise` puts a file or directory at a second path where specific values are swapped out. The main use is keeping personal information away from AI tools. You point the AI at the guise, where your name, address and phone number are placeholders. The original stays an ordinary file with the real data, and edits made through the guise flow back into it.

```text
~/Documents/resume.md            ~/ai/resume.md  (the guise)
─────────────────────            ───────────────────────────
# Matthew Wiltzius               # {{pi.firstname}} {{pi.lastname}}
Austin · 512-555-0199      ⇄     {{pi.city}} · {{pi.phone}}
```

Nothing is copied: the guise is computed when it's read and written back when it changes. It works with any editor, AI agent or command-line tool, because to them it's just a file.

## Install

**macOS** (Homebrew, builds from source):

```bash
brew install mwiltzius/guise/guise
```

**Linux**: download the `.deb`, `.rpm`, `.apk` or Arch package from the [releases page](https://github.com/mwiltzius/guise/releases). The package depends on `fuse3`, so your package manager installs it automatically.

**From source** (Go 1.27+):

```bash
go install github.com/mwiltzius/guise/cmd/guise@latest
```

## Quick start

```bash
guise vault init                              # an encrypted vault for your values
guise vault set firstname                     # prompts for the value (never on the command line)
guise vault set lastname
guise new ~/Documents/resume.md ~/ai/resume.md
cat ~/ai/resume.md                            # placeholders, ready for an AI to edit
```

That's it. There is no mount step: `guise new` starts the background process if needed, and the guise is usable immediately. Optionally let it start at login (`guise autostart on`).

## How it works

- **Placeholders** are named, like `{{pi.lastname}}`. An AI can move, duplicate or add them anywhere, and each renders to the right value in the original.
- **Spellings**: `{{pi.lastname:upper}}`, `{{pi.lastname:lower}}`, and numbered variants such as `{{pi.lastname:2}}` for any other casing, which are recorded automatically.
- **Several values of one kind**: `{{pi.lastname.2}}`. Names can be grouped however you like, e.g. `ref1.phone`.
- **Detection**: emails, phone numbers, SSNs and card numbers that aren't in your vault are hidden anyway, as `{{pi.unreviewed.email.1}}`. Run `guise review` to name them or dismiss them.
- **Directories**: `guise new ~/Documents/job ~/ai/job` guises every text file and Word document inside. Other binary files, symlinks and vaults are never exposed. Files deleted through a directory guise go to the Trash.
- **Word documents** (`.docx`, `.docm`, `.dotx`, `.dotm`): body text, headers and footers, footnotes, comments, tracked changes, authors, document properties, image alt text and hyperlinks are all transformed. Word often splits a word across several formatting runs; guise handles that. As a final check, a document is withheld from the guise entirely if any vault value would remain anywhere in it.
- **Fill mode**: `guise new --fill letter.md ~/out/alice.md --vault alice.toml` works the other way round. The original holds placeholders and the guise shows one person's values, so you can have one letter with many personalized guises.

### Vaults and rules

Vaults are TOML files, either encrypted with [age](https://age-encryption.org) (the default) or plain (`--plain`) for values you'd like to edit by hand. A guise can stack several vaults (`--vault personal.age --vault work.age`); when a name appears in more than one, the first vault wins.

Rules control detection per guise, per project (`.guise/config.toml`) or per path:

```toml
ignore = ["support@example.com"]   # never flag this

[expose]
city = true                        # show the real city (e.g. for job targeting)

[detectors]
phone = false

[[path]]
match = "drafts/**"
[path.expose]
firstname = true
```

## Commands

```text
guise new <target> <path> [--fill] [--vault V]... [--expose NAME]...
guise rm <path>                     remove a guise (the original is untouched)
guise list
guise vault init|set|get|list|rm|pattern
guise review [promote <name> [as <name>] | dismiss <name>]
guise status | unlock | start | stop | autostart [on|off]
```

## Platform notes

- **macOS** serves guises through a local NFS mount using the system's own client, so no driver is needed. The first time an app opens a guise, macOS asks whether it may access files on a network volume. Allow it once per app.
- **Linux** uses FUSE (`fuse3`, mounted as you, no root).
- After a restart, guises on encrypted vaults need `guise unlock`. A notification reminds you.

## Security model

A guise keeps your values out of what an AI *reads through the guise*. It doesn't stop a program that can read your whole disk from opening the original, the vault, or the mount's real data source. Run AI agents with access limited to their working directory, which is a setting most agents offer. Vaults are encrypted by default, and passphrases only ever travel over pipes or a private socket, never in command lines or environment variables.

Known limitations: plain text and Word documents only for now (other formats are hidden in hide mode). Some tools replace a file guise's symlink instead of writing through it (e.g. GNU `sed -i`); `guise` notices within two seconds, applies the edit and restores the link. On macOS, `cp` into a guise may report that it couldn't copy extended attributes after the folder was listed; the file's contents are copied correctly.

## Tests

```bash
tests/run.sh          # everything
tests/run.sh unit     # Go unit tests (they live next to the code)
tests/run.sh e2e      # end-to-end tests over real mounts, in a throwaway HOME
```

The end-to-end tests need `python3`, and on Linux also `fuse3` (plus `vim` for the vim checks). Set `GUISE_DEBUG=1` to log every NFS request and failing filesystem operation to the background process's log.

## License

[GPL-3.0](LICENSE)
