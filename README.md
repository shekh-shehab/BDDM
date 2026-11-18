# BDDM

BDDM is a Windows download manager written in Go. A browser extension for Firefox and Chromium-based browsers (Chrome, Edge, Brave, and others) can send downloads and videos to it.

## Install

1. Open the [latest Release](https://github.com/shekh-shehab/BDDM/releases/latest) and download `BDDM-Setup.exe`.
2. Run the setup file. It installs for your Windows account only. Windows may show "Windows protected your PC" because the setup is not signed with a paid certificate — click **More info**, then **Run anyway**.
3. Add the browser extension. The Release also includes:
   - `BDDM-firefox-signed.xpi` — drag this into Firefox (or use about:addons → gear → **Install Add-on From File**). It is signed by Mozilla but is not on addons.mozilla.org.
   - `BDDM-chromium-extension.zip` — unzip it, then in Chrome, Edge, or Brave open the extensions page, turn on **Developer mode**, and choose **Load unpacked**.

Full steps are in `INSTALL.txt` in this repository and in each Release.
