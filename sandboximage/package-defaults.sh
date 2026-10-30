#!/bin/sh
# Package-manager defaults against malicious releases (ADR 0022): a release
# cooldown and no install scripts. Run as root at build; ROOT writes into
# another tree.
#
# The tools come from mise, in the home, so a tool's "global" config (npm's
# $PREFIX/etc/npmrc, Ruby's sysconfdir gemrc) is inside its install
# directory. Only these are read from outside the home, and each stays below
# the project's and the user's own config:
#  - npm: the global npmrc, at the path NPM_CONFIG_GLOBALCONFIG names;
#  - uv: /etc/uv/uv.toml.
# Bundler has no such file: BUNDLE_COOLDOWN is set in the environment, which
# a project's .bundle/config still overrides.
set -eu
ROOT=${ROOT:-}
# Malicious releases are usually found and pulled within hours to days; a
# week also covers the slower cases at little cost.
COOLDOWN_DAYS=7

# No pip default: pip 26.0, which Python 3.14.4 bundles, rejects a duration.
install -d -m 755 "$ROOT/etc/iwato" "$ROOT/etc/uv" "$ROOT/etc/profile.d"
# min-release-age: npm 11.10+ (older npm warns that it is unknown).
printf 'min-release-age=%s\nignore-scripts=true\n' "$COOLDOWN_DAYS" > "$ROOT/etc/iwato/npmrc"
# A relative exclude-newer: uv 0.9.17+.
printf 'exclude-newer = "%s days"\n' "$COOLDOWN_DAYS" > "$ROOT/etc/uv/uv.toml"
# Login and interactive shells read it before the owner's ~/.profile and
# ~/.bashrc, which may change or unset the variables.
cat > "$ROOT/etc/iwato/package-defaults.sh" <<EOF
export NPM_CONFIG_GLOBALCONFIG="\${NPM_CONFIG_GLOBALCONFIG:-/etc/iwato/npmrc}"
export BUNDLE_COOLDOWN="\${BUNDLE_COOLDOWN:-$COOLDOWN_DAYS}"
EOF
chmod 644 "$ROOT/etc/iwato/npmrc" "$ROOT/etc/uv/uv.toml" "$ROOT/etc/iwato/package-defaults.sh"
ln -sf /etc/iwato/package-defaults.sh "$ROOT/etc/profile.d/iwato-packages.sh"
echo '[ -r /etc/iwato/package-defaults.sh ] && . /etc/iwato/package-defaults.sh' >> "$ROOT/etc/bash.bashrc"
