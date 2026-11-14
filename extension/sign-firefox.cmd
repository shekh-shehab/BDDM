@echo off
rem Signs the Firefox extension with Mozilla (addons.mozilla.org, "unlisted":
rem not published, only signed) so it installs permanently in normal Firefox.
rem
rem Needs Node.js and your own AMO API keys:
rem   https://addons.mozilla.org/developers/addon/api/key/
rem Run from the BDDM folder:
rem   set WEB_EXT_API_KEY=user:12345:67
rem   set WEB_EXT_API_SECRET=your-secret
rem   extension\sign-firefox.cmd
rem The signed .xpi is written to extension\signed\.
if "%WEB_EXT_API_KEY%"=="" (echo Set WEB_EXT_API_KEY and WEB_EXT_API_SECRET first. & exit /b 1)
cd /d "%~dp0"
python build.py firefox || exit /b 1
npx --yes web-ext sign --channel=unlisted --source-dir dist\firefox --artifacts-dir signed
