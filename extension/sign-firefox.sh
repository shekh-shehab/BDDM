#!/bin/sh
# Signs the Firefox extension with Mozilla (AMO "unlisted": not published,
# only signed). Needs Node.js and AMO API keys
# (https://addons.mozilla.org/developers/addon/api/key/):
#   WEB_EXT_API_KEY=user:12345:67 WEB_EXT_API_SECRET=... extension/sign-firefox.sh
# The signed .xpi is written to extension/signed/.
set -e
[ -n "$WEB_EXT_API_KEY" ] && [ -n "$WEB_EXT_API_SECRET" ] || { echo "Set WEB_EXT_API_KEY and WEB_EXT_API_SECRET first."; exit 1; }
cd "$(dirname "$0")"
python3 build.py firefox
npx --yes web-ext sign --channel=unlisted --source-dir dist/firefox --artifacts-dir signed
