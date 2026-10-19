# Files and the editor

[日本語](ja/files-and-editor.md)

Both show the sandbox's home `/home/dev`. In a sandbox whose image does not follow Iwato's convention, they show OpenShell's working directory instead, with the note "Opens the working directory (…)". Recreating the sandbox with the standard image gives the home ([sandboxes](sandboxes.md#your-own-images-and-the-sandbox-convention)).

## Files: the home as a network drive

Files (in the card's ⋯ menu) opens the home as a network drive (WebDAV) in Windows Explorer, the macOS Finder or a WebDAV app on a phone. Changes reach the sandbox at once. Iwato keeps no files of its own: it translates WebDAV to the sandbox's SFTP.

Create a credential per device under App passwords and URLs (its value is shown once), and revoke it with **Revoke** when no longer needed. Each one covers this sandbox or all sandboxes, read-write or read-only. Two kinds:

- **URL for this PC** (Windows): a secret path `\\127.0.0.1@…` to paste into Explorer's address bar or "Map network drive". No password is asked (the URL is the key), and it works only from this PC (127.0.0.1). Do not share it
- **App password**: for Windows, a Mac and phones over the tailnet's HTTPS, when Iwato runs with `-public-origin` ([phone access](publishing.md#use-iwato-from-your-phone)). A Mac running Iwato can also use it at `http://127.0.0.1:PORT/dav/NAME/`. The user name is the app password's ID and the password is its value. The tab shows the URLs and the steps for each:
  - Windows: Explorer → right-click "This PC" → "Map network drive", paste the URL, tick "Connect using different credentials"
  - Mac: Finder ⌘K (Go → Connect to Server), paste the URL, enter the ID and password
  - Phone: a WebDAV app (iPhone: Documents, FE File Explorer, …; Android: Material Files, Solid Explorer, …) with the phone on the tailnet. The iPhone Files app's "Connect to Server" does not speak WebDAV

Notes:

- Windows' WebDAV client cannot handle files larger than about 48 MB by default. If the first connection takes about 20 seconds, turning off "Automatically detect settings" in Internet Options makes it faster
- To stop the Finder writing `._*` and `.DS_Store` files, run `defaults write com.apple.desktopservices DSDontWriteNetworkStores -bool true` on the Mac
- While the sandbox is stopped, the drive shows only `停止中です.txt` and refuses writes

## The editor

The **Editor** tab opens text files of the home and saves them in place, for when opening the drive in VS Code would be too much.

- Open several files in tabs; unsaved files are marked
- On a wide screen, the Side by side button in the tab row shows the editor on the left and the terminal on the right. Drag the line between them to change the widths; clicking a pane gives it the keyboard. The Policy and Ports tabs and the ⋯ views take their place until you go back to Terminal or Editor. This browser remembers the choice; a narrow window shows the tabs again
- Ctrl+P (⌘P on a Mac) opens a file by part of its name (fuzzy, like VS Code's quick open; hidden paths come last unless you type the leading `.`); ↑↓ and Enter, Esc to close. Ctrl+S (⌘S) saves. On a phone, use the buttons Open a file and Save
- Folder narrows the search to a folder under the home (for example `work/iwato`; at first `~/work` when it exists), remembered per sandbox in this browser
- The folder's tree is on the left on a PC (**Tree** shows and hides it) and in a drawer on a phone. A folder is read when you open it (up to 2,000 entries; **Refresh** reads it again); open folders are remembered, and open files are marked. Hidden files are shown; heavy folders such as `node_modules` are not expanded by themselves. Symbolic links are marked, and only links to files open
- The file list comes from `rg --files --hidden` run in the sandbox as the sandbox user (it follows `.gitignore` and leaves out `.git`, `node_modules`, `.cache` and similar; up to 50,000 files, cached for 30 seconds; Refresh the list reads it again). Images without `rg` are walked over SFTP
- Opens UTF-8 text up to 5 MB, and images the browser can show up to 20 MB (PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, ICO, recognised by content). Images are view-only, with the name, the size in pixels and bytes, a click to switch between fit and 100 %, and **Reload**; an SVG can also be opened with **Open as text**. Other binary files do not open
- Saving writes a temporary file and then replaces the file, keeping its permissions. If the file changed elsewhere after you opened it, saving stops and offers **Reload** or **Overwrite**
- Syntax highlighting by the file's name: JavaScript, TypeScript (JSX, TSX), JSON, HTML (also SVG and XML), CSS, Markdown, Python, Go, Rust, YAML, shell scripts, Dockerfile, SQL, Ruby and TOML; other files are plain text. Line numbers, bracket matching, folding, and an undo history per open file. Tab indents (a tab in Go files and Makefiles, two spaces elsewhere; selected lines as a block), Shift+Tab leaves the text, Enter keeps the indentation
- Ctrl+F (⌘F) searches and replaces in the file (case, regular expressions, whole words); Ctrl+G (⌘G) or F3 finds the next match, Ctrl+Alt+G (⌘⌥G) goes to a line. No completion
