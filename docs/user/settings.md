# Settings and reset

[日本語](ja/settings.md)

Open **Settings** from the sidebar. They have:

- **言語 / Language**: automatic (the browser's language: Japanese, otherwise English), 日本語 or English. Remembered per browser. Some messages are still Japanese only
- **Startup checks**: WSLc or the Docker engine, its settings and the configuration, each OK, a warning or a problem, with how to fix it. Check again runs them again
- **The standard image**: where it comes from and when it was created and pulled; with a development build, **Update the standard image**. See [sandboxes](sandboxes.md#where-the-image-comes-from)
- **Publishing ports**: this PC only, Tailscale or off, with the Tailscale secret, the owner logins and nodes. See [publishing](publishing.md)
- **Signed-in devices**: each browser signed in to Iwato, with when it signed in and was last used. Sign out signs this browser out; Revoke signs another one out and closes its terminals; Sign out everywhere else does that for every other one. A sign-in otherwise lasts until it expires (`-session-hours`)
- **Delete everything**, at the bottom (below)

## The config folder and `iwato.json`

Iwato keeps everything of its own in the folder of `iwato.json` (the folder you start it in, or `-config`):

| File | What |
|---|---|
| `iwato.json` | the configuration and the sandboxes' settings |
| `iwato.log`, `iwato.log.1` | the log |
| `image-build.log` | image pulls and builds |
| `presets\*.yaml` | your presets ([network policy](network-policy.md#your-own-presets)) |
| `profiles\*.yaml` | your provider profiles ([providers](providers.md#custom-providers)) |
| `ingress-secret.bin`, `ingress\` | the Tailscale secret (DPAPI) and node states |
| certificates, sessions, Web Push keys, app passwords | secrets Iwato made |

Iwato writes `iwato.json` itself; most of it is set from the page. Settings that have no screen yet:

- `"termTrace": true`: record terminal output ([terminals](terminals.md#terminal-recordings))
- `"wslc"`: the full path of `wslc.exe` when it is not on `PATH`
- per-port publishing settings ([publishing](publishing.md#per-port-settings))

Stop Iwato before editing `iwato.json` by hand.

## Delete everything

Delete everything at the bottom of the settings deletes everything Iwato made and returns to the state of a first run. Type `reset` to confirm.

Deleted:

- all sandboxes and their volumes (also volumes left from deleted sandboxes)
- the OpenShell gateway, with the providers' credentials, the policies and the certificates, and its WSLc container, volume and network
- Iwato's images (the standard image and the gateway image, only those Iwato prepared)
- the secrets in the config folder: the gateway's and driver's certificates, the Tailscale secret and nodes (logged out first), the Web Push keys and subscriptions, every device's sign-in, the files' app passwords and secret URLs; on macOS also the Keychain key that encrypts these secrets
- sign-ins to providers in progress (Atlassian, Claude, xAI): canceled first

Kept: `iwato.exe`, the logs, the WSLc session (its VHDX) itself, other images you gave sandboxes, OpenShell's runtime images. Your own presets and provider profiles (`presets\`, `profiles\`) and the settings in `iwato.json` (publishing and so on) are also kept by default; a checkbox in the dialog deletes them too.

The VHDX does not shrink: the blocks of the deleted volumes stay in it until it is compacted or removed, so data from the sandboxes can still be recovered from that file. To be rid of them, remove the WSLc session (or compact its VHDX) after the reset.

When it ends, every device is signed out. Open the new sign-in URL that the console prints; Iwato prepares the gateway and the images again, which takes a few minutes. It cannot run while the images or the gateway are being prepared.
