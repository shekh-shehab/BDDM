; BDDM installer (NSIS 3). Installs for the current user only: no admin
; rights, files in %LOCALAPPDATA%\Programs\BDDM. Settings and download
; history stay in %APPDATA%\BDDM.
;
; Build (from the BDDM folder, after building build\BDDM.exe and the
; extensions):   makensis installer\BDDM.nsi

Unicode true
Target amd64-unicode
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "Sections.nsh"
!insertmacro GetParameters
!insertmacro GetOptions

!ifndef VERSION
  !define VERSION "1.4.11"
!endif
!define APP "BDDM"
!define APPNAME "BDDM Download Manager"
!define UNKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\BDDM"
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"

Name "${APPNAME}"
OutFile "..\BDDM-Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\BDDM"
InstallDirRegKey HKCU "Software\BDDM" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "BDDM ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileDescription" "BDDM Download Manager setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "BDDM"

!define MUI_ICON "BDDM.ico"
!define MUI_UNICON "BDDM.ico"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TEXT "This installs BDDM ${VERSION} for your Windows account (no administrator rights needed).$\r$\n$\r$\nIf BDDM is running, it is closed first; running downloads are paused and continue when BDDM starts again.$\r$\n$\r$\nYour settings and download history are kept."
!insertmacro MUI_PAGE_WELCOME
!define MUI_COMPONENTSPAGE_NODESC
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TEXT "BDDM is installed.$\r$\n$\r$\nNext, add the BDDM extension to your browser (Chrome, Edge, Brave, Firefox) so downloads go to BDDM. The guide shows how; it takes a minute."
!define MUI_FINISHPAGE_RUN "$INSTDIR\BDDM.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start BDDM now"
!define MUI_FINISHPAGE_SHOWREADME
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Show how to add the browser extension"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION ShowExtensionGuide
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; Asks a running BDDM to save and quit, then makes sure it is gone.
!macro StopBDDM EXE
  IfFileExists "${EXE}" 0 +2
    nsExec::Exec '"${EXE}" -quit'
  nsExec::Exec 'taskkill /IM BDDM.exe /F'
  Sleep 500
!macroend

Section "BDDM (required)" SecMain
  SectionIn RO
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File "..\build\BDDM.exe"
  !insertmacro StopBDDM "$PLUGINSDIR\BDDM.exe"

  SetOutPath "$INSTDIR"
  File "..\build\BDDM.exe"
  File "..\INSTALL.txt"
  File "BDDM.ico"

  ; The browser extension: a folder for "Load unpacked" (Chrome, Edge,
  ; Brave, Opera, Vivaldi) and the Firefox package.
  RMDir /r "$INSTDIR\extension"
  SetOutPath "$INSTDIR\extension\chromium"
  File /r "..\extension\dist\chromium\*.*"
  SetOutPath "$INSTDIR\extension"
  File "..\BDDM-firefox.xpi"
  File /nonfatal "..\BDDM-firefox-signed.xpi" ; only after extension\sign-firefox
  SetOutPath "$INSTDIR"

  CreateDirectory "$SMPROGRAMS\BDDM"
  CreateShortcut "$SMPROGRAMS\BDDM\BDDM.lnk" "$INSTDIR\BDDM.exe" "" "$INSTDIR\BDDM.ico"
  CreateShortcut "$SMPROGRAMS\BDDM\Browser extension guide.lnk" "$INSTDIR\INSTALL.txt"
  CreateShortcut "$SMPROGRAMS\BDDM\Browser extension folder.lnk" "$INSTDIR\extension"
  CreateShortcut "$SMPROGRAMS\BDDM\Uninstall BDDM.lnk" "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "Software\BDDM" "InstallDir" "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "${UNKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNKEY}" "Publisher" "BDDM"
  WriteRegStr HKCU "${UNKEY}" "DisplayIcon" "$INSTDIR\BDDM.exe"
  WriteRegStr HKCU "${UNKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "${UNKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNKEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${UNKEY}" "EstimatedSize" 20000
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortcut "$DESKTOP\BDDM.lnk" "$INSTDIR\BDDM.exe" "" "$INSTDIR\BDDM.ico"
SectionEnd

Section "Start BDDM with Windows (in the background)" SecAutostart
  WriteRegStr HKCU "${RUNKEY}" "BDDM" '"$INSTDIR\BDDM.exe" -nowindow'
SectionEnd

Var StartAfter

Function .onInit
  StrCpy $StartAfter "0"
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "/run" $1
  IfErrors +2
    StrCpy $StartAfter "1"

  ; Silent upgrade of an existing install: leave the desktop shortcut
  ; and "Start with Windows" choices as the user already set them.
  IfSilent 0 done_init
  ReadRegStr $2 HKCU "Software\BDDM" "InstallDir"
  IfErrors done_init
    !insertmacro UnselectSection ${SecDesktop}
    !insertmacro UnselectSection ${SecAutostart}
  done_init:
FunctionEnd

Function .onInstSuccess
  ${If} $StartAfter == "1"
    Exec '"$INSTDIR\BDDM.exe"'
  ${EndIf}
FunctionEnd

Function ShowExtensionGuide
  ExecShell "open" "$INSTDIR\INSTALL.txt"
  ExecShell "open" "$INSTDIR\extension"
FunctionEnd

Section "Uninstall"
  !insertmacro StopBDDM "$INSTDIR\BDDM.exe"
  Delete "$INSTDIR\BDDM.exe"
  Delete "$INSTDIR\INSTALL.txt"
  Delete "$INSTDIR\BDDM.ico"
  RMDir /r "$INSTDIR\extension"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  RMDir /r "$SMPROGRAMS\BDDM"
  Delete "$DESKTOP\BDDM.lnk"
  DeleteRegValue HKCU "${RUNKEY}" "BDDM"
  DeleteRegKey HKCU "${UNKEY}"
  DeleteRegKey HKCU "Software\BDDM"
  ; Settings, history and the video tools are in %APPDATA%\BDDM.
  ; Downloaded files are never touched.
  IfSilent done
  MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "Also delete BDDM's settings, download history and video tools ($APPDATA\BDDM)?$\r$\n$\r$\nYour downloaded files are not deleted either way." IDNO done
  RMDir /r "$APPDATA\BDDM"
  done:
SectionEnd
