# Files (WebDAV over SFTP)

The **Files** tab opens a sandbox's home `/home/dev` (OpenShell's working directory for images without Iwato's daemon, below) as a network drive in Windows Explorer, the macOS Finder or a WebDAV app on a phone. Iwato serves WebDAV at `/dav/` and translates it to the sandbox's SFTP. Iwato keeps no files of its own.

## How it works

- SFTP: the SSH server of OpenShell's supervisor accepts the `sftp` subsystem, and an SFTP v3 server inside the boundary process answers (`crates/openshell-sandbox/src/sftp.rs`). Its root is the workdir and nothing outside is reachable (`..` is refused, `openat2(RESOLVE_IN_ROOT)`). Iwato runs a `github.com/pkg/sftp` (v1.13.11) client over an SSH connection on the same gateway relay as the terminals (`sdk.SSH().Tunnel`, `internal/openshell/client/sftp.go`). Paths are sent relative to the root: OpenShell reads an absolute path that starts with the workdir (e.g. `/sandbox/…`) as one inside the root, so `/sandbox/sandbox/…` would be ambiguous
- Pool (`internal/dav/pool.go`): per sandbox one relay (the gateway allows 20 per sandbox) and at most 4 SFTP channels. OpenShell handles a channel's requests in order, so a request takes an idle channel or opens another. Channels close after 5 minutes unused, on start, stop and delete (`endOp`), and when a credential is revoked. A broken channel is reopened
- Stalled channels: pkg/sftp requests have no timeout, so a request the server never answers waits forever, and so do the requests behind it on that channel; new requests keep piling onto it, so it never goes idle and never closes. The pool therefore counts the request and response packets of each channel (SFTP v3 answers every request once), and when requests are waiting and nothing has come from the server for 1 minute (`Pool.Stall`), it closes the whole connection. The waiting requests fail and the next one reconnects. Time spent writing a GET to a slow HTTP client does not count (no SFTP request is outstanding then). The log gets one line such as `files: NAME: SFTP channel N of M stalled: K requests unanswered, nothing from the server for 1m0s; in use by stat a.txt (1m5s), get b.png (2m3s); closing the connection`
- Opening a channel (SSH session, subsystem, SFTP version exchange) may take 30 seconds (`Pool.OpenTimeout`). After that Iwato logs `files: NAME: opening an SFTP channel took over 30s; closing the connection`, treats the connection as broken and reconnects once. No lock is held while opening, so other requests continue on the open channels
- WebDAV (`internal/dav/fs.go`, `golang.org/x/net/webdav`):
  - PUT writes to a sibling `.NAME.iwato-XXXXXXXXXXXX` and replaces the file only once the body is complete (an interrupted upload removes the temporary file and leaves the original alone). OpenShell's rename does not overwrite (`RenameFlags::NOREPLACE`), so the original is first moved to `.NAME.iwato-old-…`, the new file put in place, and the old one removed (or restored on failure). Temporary files are hidden from listings
  - Listings (PROPFIND) take the Content-Type from the extension (no file is opened). Stat results are cached within a request, so a Depth 1 listing is one stat and one readdir
  - GET supports Range (`http.ServeContent`). Reads use a 1 MiB buffer and pkg/sftp's concurrent reads
  - LOCK is `webdav.NewMemLS()` (in Iwato only, lost on restart). The Finder mounts read-only when LOCK is unavailable
  - PROPPATCH (Windows' Win32 times and attributes) is kept in memory; only `Win32LastModifiedTime` also sets the file's modification time
  - PROPFIND with `Depth: infinity` is 403 (`propfind-finite-depth`); no Depth means 1
  - Delete does not follow symbolic links (it does not empty a linked directory)
  - The Finder's `._*` and `.DS_Store` are written as ordinary files. To stop them, run `defaults write com.apple.desktopservices DSDontWriteNetworkStores -bool true` on the Mac
- Stopped (also not yet created, or starting): an empty folder with only `停止中です.txt` is shown (so the drive stays mounted), and writes get 503. For 3 seconds after learning that a sandbox is stopped, Iwato does not ask the gateway again
- OpenShell allows 256 open handles per channel. A WebDAV request opens at most one file, and there are 4 channels, so this is not reached in practice

## Root: the home (the daemon's SFTP), or OpenShell's working directory

With the image convention (ADR 0005), the standard image carries Iwato's daemon, whose `iwato-entrypoint sftp -root /home/dev` (`internal/rootsftp`, confined with `os.Root`: no `..`, no link out of the home) Iwato starts as a TTY-less exec per SFTP channel (`internal/openshell/client/daemon_sftp.go`). Iwato uses it when the daemon's list says `sftp` (`openshell_files.go`, `DialFiles`, `FilesRoot`); the files tab and the editor note when it falls back to OpenShell's SFTP below ("Opens the working directory ({path})."; recreating the sandbox with the standard image gives the home). The standard image's `WORKDIR` is `/sandbox`, and terminals start in `~/work`.

OpenShell's SFTP root (the fallback) is the sandbox's workdir (`crates/openshell-supervisor-process/src/ssh.rs` passes `workdir: None` for `RuntimeHelper::Sftp`, and `crates/openshell-sandbox/src/boundary_exec.rs` uses `spec.workdir.or(base_workdir)`). The driver takes the workdir from the image's `WORKDIR` (`/sandbox` when empty or `/`; `resolveWorkspace`). The standard image before the convention set `WORKDIR /home/dev`, so for sandboxes made from it the fallback shows the home volume too; images without `WORKDIR` get `/sandbox`.

- Like the Docker driver, the WSLc driver refuses a mount on the working directory or a parent of it, except a writable volume on the working directory itself, for older images whose `WORKDIR` is the home. The volume's owner is set to the sandbox user at every start (`volumeowner.go`), so OpenShell's workdir check (the user can write it) passes
- The working directory is fixed at creation (driver state `workspace-root`) and does not change on restart. A sandbox created from an older image keeps `/sandbox` until it is recreated (menu **Recreate**, `recreate.go`). Own images without `WORKDIR` (or with `/`) also get `/sandbox` (the home is not shown); with `WORKDIR /home/dev` they show the home

## Image paste (`paste.go`)

Pasting or dropping an image into a terminal (on a phone, the **Image** key in the key row) writes it into the sandbox through the same SFTP pool.

- Page (`web/term.js`, `web/app.js`): when the clipboard has text (`text/plain`), only the text is sent (spreadsheet cells come with a picture of themselves). Only when there is no text but a file (a screenshot, a copied image) and for dropped files, the image goes to `POST /api/sandboxes/NAME/paste-image` (body: the image bytes, Content-Type its type; CSRF etc. as for other changes). The returned path is pasted the way terminal pastes are (bracketed paste), followed by a space. The path is escaped with backslashes to be one shell word (as local terminals do on drop; Claude Code reads backslashes, not quotes)
- Accepted: PNG, JPEG, GIF and WebP (both the Content-Type and the content, `http.DetectContentType`; the extension follows the content), up to 20 MB. 409 while stopped
- Location: `.cache/iwato/paste/<UTC time>-<random>.<ext>` under the SFTP root (the home) (directory 0700, file 0600, owned by the workload's user). Each write removes files beyond the newest 50 and files older than 7 days (never the one just written)
- Absolute path: with the daemon's SFTP, the home. With OpenShell's: it answers realpath of the root with `/` (`sftp.rs`), so SFTP cannot tell it. An exec without a workdir starts in the same OpenShell workdir as SFTP, so after writing Iwato execs as the workload and runs `cd -P . && test -r <file> && pwd` (which also checks that the workload can read it). `/home/dev` with the standard image from before the convention (`WORKDIR /home/dev`); `/sandbox` with still older images or images without `WORKDIR`
- e2e: `TestOpenShellFilesE2E` writes with the same function (`pasteImage`) and checks through a workload exec that the returned path is readable and has the same sha256

## Authentication and entry (`files.go`)

- URLs: `/dav/` lists the visible sandboxes (read-only); `/dav/<name>/…` is `…` under that sandbox's files root (the home; the working directory for images without Iwato's daemon); `/dav/<token>/<name>/…` is the secret URL for this PC
- `/dav/` branches off before the admin UI's Origin, cookie and CSRF checks (WebDAV clients send neither Origin nor cookies). It keeps the Host check against DNS rebinding (only `127.0.0.1:PORT`, `localhost:PORT` and the `-public-origin` host) and refuses requests from other pages (a different Origin, `Sec-Fetch-Site: cross-site`). Responses carry `Content-Security-Policy: … sandbox` so that a sandbox's HTML cannot act as an Iwato page
- App passwords: Basic authentication. The user name is the ID (`iwato-xxxxxx`), the password 32 random bytes (lowercase base32, 52 characters). They work over the tailnet's HTTPS (tailscale serve) and on this PC's http://127.0.0.1
- Secret URL: `http://127.0.0.1:PORT/dav/<token>/<name>/`. Windows' WebClient by default (`BasicAuthLevel=1`) sends no Basic credentials over plain http, so for Windows without tailscale serve the URL itself is the key (the registry is left alone). It is accepted only from 127.0.0.1 (loopback), with Host `127.0.0.1:PORT` or `localhost:PORT`, and without any proxy header (`X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Forwarded`, `Tailscale-User-Login`, `Tailscale-Headers-Info`). tailscale serve also connects from 127.0.0.1 but adds `X-Forwarded-For` and the like, which tells them apart
- Both have a scope (one sandbox or all) and a permission (read-write or read-only). Read-only answers 403 to PUT, DELETE, MKCOL, MOVE, COPY, PROPPATCH, LOCK and UNLOCK
- Only SHA-256 hashes are stored (`dav-keys.json` in the config folder, 0600). The value is shown once, right after creation. Revoking takes effect at once and closes open connections. The last use time and user agent are recorded (the file is written at most once a minute)
- Answered without authentication: OPTIONS (`/dav/…` and `/`; Windows asks before authenticating) and PROPFIND of `/dav` from this PC (an empty folder; Windows first checks the first segment, `dav`, as a "share")
- Wrong credentials are refused after 0.5 seconds
- Authenticated requests lift the `http.Server` ReadTimeout (15 seconds) for large uploads

## The policy's read_only does not apply to SFTP (checked 2026-10-08)

SFTP runs as a task inside the boundary process, and the policy's Landlock applies only to the workload (child processes; `process.rs`, `sandbox/linux/mod.rs`). `TestOpenShellFilesE2E` (`MODE=tls internal/openshell/wslc/e2e.sh`) checked this by putting `/sandbox` (the working directory then) in the policy's `read_only` with `include_workdir: false` (the test now puts the home, the working directory, in `read_only` and leaves it out of `read_write`):

    read_only: workload write to /sandbox: DENIED; SFTP (WebDAV PUT 201) write: true
    read_only: NOT enforced for SFTP (the workload is denied, SFTP writes)

Landlock denies the workload's write, but SFTP (Iwato's WebDAV PUT) writes. Only Unix permissions decide what SFTP may read and write (in the e2e, files written over SFTP are owned by the workload's user).

Iwato does not imitate read_only (it does not make the drive read-only from the policy's read_only). To get a read-only drive, create the credential as read-only. Landlock grants are a union, so while the working directory is read-write (the default `include_workdir: true`; the home is also read-write as a volume), putting a path below it in `read_only` does not restrict the workload either.

## Checking on real hardware

Setup: start the new exe with `-public-origin https://<host>.ts.net` and expose the 127.0.0.1 port over HTTPS with `tailscale serve` (as for the phone UI). Start a sandbox and open the **Files** tab.

### Windows (tailnet HTTPS)

1. In the tab, **Create** with a name (e.g. a work PC), app password, scope and permission. Note the ID and password
2. In Explorer, right-click "This PC" → "Map network drive". Folder `https://<host>.ts.net/dav/<name>/` (or `\\<host>.ts.net@SSL\dav\<name>`), tick "Connect using different credentials"
3. User name: the ID; password: the value. The drive opens and shows the home (`/home/dev`)
4. Check: open and save files (Notepad, Office), copy in and out, create, rename and delete folders, and see the changes with `ls -la` in the sandbox's terminal
5. Files over 48 MB fail with error 0x800700DF by default (Windows' `FileSizeLimitInBytes`), not an Iwato bug
6. Stopping the sandbox leaves only `停止中です.txt` visible and writes fail; after a start the contents are back
7. After **Revoke**, the next operation fails

### Windows (this PC, no tailscale serve)

1. **Create** with "URL for this PC"
2. Paste the shown `\\127.0.0.1@17600\dav\<token>\<name>` into Explorer's address bar or the "Map network drive" folder. No credentials are asked
3. Check 4, 6 and 7 above, and that the WebClient service starts if it was stopped (manual, trigger start)
4. The same URL opened from another tailnet device as `https://<host>.ts.net/dav/<token>/…` is refused (403)

### Mac (Finder)

1. Create an app password
2. In the Finder, ⌘K, `https://<host>.ts.net/dav/<name>/`, "Connect". Enter the ID and password and save them in the keychain
3. It connects read-write (a read-only credential connects read-only). Copy, rename, delete, Quick Look; `._*` and `.DS_Store` appear in the sandbox as ordinary files

### Phones

- iPhone: the Files app's "Connect to Server" is SMB only. Add a WebDAV server in Documents (Readdle), FE File Explorer, Owlfiles or similar, with the URL, ID and password. The phone must be on the tailnet
- Android: likewise with Material Files, Solid Explorer, CX File Explorer or similar
- Check listing, viewing images and videos (seeking uses Range), upload and delete

## Tests

- `go test ./internal/dav/`: against pkg/sftp's in-memory server (rename does not overwrite, as in OpenShell) and a real-directory server: PROPFIND, Range, PUT replacement and interruption, MKCOL, MOVE/COPY, DELETE, LOCK, read-only, stopped, PROPPATCH, idle close, reconnecting a broken connection, a server that stops answering (fails on the timeout, reconnects next time)
- `go test -tags uitest -run Paste .`: image paste (the fake backend's in-memory SFTP: types, size, CSRF, stopped, removing old files)
- `go test -tags uitest -run Files .`: app passwords, the secret URL (loopback only, proxy headers, Host), scope, read-only, revoke, DNS rebinding, other pages, CSP, stopped, slow uploads
- `node scripts/ui-behavior-tests.cjs`: URL building, the tab, creation (shown once), revoke, stopped
- `scripts/files-check.cjs [--phone]`: drives the tab of `serve -fake` in Chromium and uses WebDAV with the created credential
- `TestOpenShellFilesE2E` (`MODE=tls internal/openshell/wslc/e2e.sh`): opens the daemon's SFTP through a real gateway (OpenShell's SFTP for a sandbox without the daemon: `TestOpenShellTerminalsSurviveRestartE2E`); the root is the home (what the workload creates in `$HOME` is visible); WebDAV read, write, replace, move, copy, delete, LOCK, stopped, and the read_only finding above
