Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####
## Include the wails tools
####
!include "wails_tools.nsh"

# Rename migration: installs of the old product ("SDK Version Control")
# registered under a different uninstall key. Note the INSTALLED executable
# was named after the old wails project name (spaces included); the
# SDKVersionControl-* spelling only ever appeared in release asset names.
!define LEGACY_PRODUCTNAME "SDK Version Control"
!define LEGACY_EXECUTABLE  "SDK Version Control.exe"
!define LEGACY_UNINST_KEY  "Software\Microsoft\Windows\CurrentVersion\Uninstall\${INFO_COMPANYNAME}${LEGACY_PRODUCTNAME}"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"
!include "FileFunc.nsh" # ${GetParent}: derive the legacy install dir from UninstallString

!define MUI_ICON "..\icon-white.ico"
!define MUI_UNICON "..\icon-white.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

# If already installed, skip the directory selection page and reuse the
# previous install path silently (read from the registry).
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipDirIfInstalled

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
InstallDir "$PROGRAMFILES64\${INFO_PRODUCTNAME}" # Default installing folder ($PROGRAMFILES is Program Files folder).
# Auto-detect previous install location from the registry (enables upgrade-in-place).
# Note: NSIS evaluates InstallDirRegKey before .onInit under the default
# (32-bit) registry view, so on x64 it never matches the 64-bit key written
# by wails.writeUninstaller; real detection happens in SkipDirIfInstalled.
InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
   # Read the uninstall key in the native 64-bit registry view:
   # wails.writeUninstaller writes it under SetRegView 64, but this
   # installer is a 32-bit (WOW64) process whose default view is the 32-bit
   # hive, so upgrade-in-place detection would otherwise miss it. The view
   # persists into page callbacks, fixing SkipDirIfInstalled below.
   SetRegView 64
FunctionEnd

# Skip the directory page on upgrade: if a previous install location of THIS
# product is found in the registry, set $INSTDIR to it and Abort (skip) the
# page. Legacy ("SDK Version Control") locations are deliberately NOT reused:
# the rename migration installs into the fresh default directory
# ($PROGRAMFILES64\svc) and removes the old location in the Section below,
# so folder name, shortcuts and registry entries all carry the new name.
Function SkipDirIfInstalled
    ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $0 != ""
        StrCpy $INSTDIR $0
        Abort
    ${EndIf}
FunctionEnd

Section
    !insertmacro wails.setShellContext

    # Restate the 64-bit registry view (also set in .onInit) so this section
    # is self-contained: the legacy-migration reads and the InstallLocation
    # write below must match the view used by wails.writeUninstaller,
    # regardless of macro side effects.
    SetRegView 64

    !insertmacro wails.webview2runtime

    # Kill any running instance of the app before overwriting the binary.
    # Windows locks a running .exe; without this, the File step fails.
    # Also kill the LEGACY executable name: pre-rename installs run
    # SDKVersionControl.exe, which would lock files in the same directory.
    # wscript.exe is a GUI-subsystem host and the VBS uses WMI, so NO console
    # window can ever flash (a bare taskkill/ExecWait/powershell would).
    File /oname=$PLUGINSDIR\svckill.vbs "svckill.vbs"
    ExecWait 'wscript.exe //B //nologo "$PLUGINSDIR\svckill.vbs"'

    # Backup the previous version before overwriting (for manual rollback).
    IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 skipBackup
        CopyFiles /SILENT "$INSTDIR\${PRODUCT_EXECUTABLE}" "$INSTDIR\${PRODUCT_EXECUTABLE}.bak"
    skipBackup:

    # Rename migration: an in-place upgrade from the legacy product leaves
    # SDKVersionControl.exe in the directory; move it aside (rollback backup)
    # so only the new svc.exe remains after wails.files installs it.
    IfFileExists "$INSTDIR\${LEGACY_EXECUTABLE}" 0 skipLegacyBackup
        CopyFiles /SILENT "$INSTDIR\${LEGACY_EXECUTABLE}" "$INSTDIR\${LEGACY_EXECUTABLE}.bak"
        Delete "$INSTDIR\${LEGACY_EXECUTABLE}"
    skipLegacyBackup:

    SetOutPath $INSTDIR

    !insertmacro wails.files

    # Ship the white-plate icon alongside the exe and point both shortcuts
    # at it explicitly. Windows shell picks the exe's first embedded icon
    # group when CreateShortcut omits the icon parameter, but that lookup is
    # fragile (icon cache, multi-group resource ordering). An explicit
    # icon file makes the desktop/Start Menu shortcut deterministically
    # white-plate, matching the macOS Dock/Launchpad appearance, while the
    # in-app window logo still loads the transparent #3 resource from the exe.
    File "..\icon-white.ico"

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\icon-white.ico"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\icon-white.ico"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    !insertmacro wails.writeUninstaller

    # Write InstallLocation so the next upgrade can detect + skip the dir page.
    WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"

    # Rename migration cleanup. Legacy shortcuts and the legacy WebView2
    # datapath are removed unconditionally (Delete/RMDir are no-ops when
    # absent) so SELF-UPDATED legacy installs — which have no registry
    # entry at all — also get their old artifacts cleared.
    Delete "$SMPROGRAMS\${LEGACY_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${LEGACY_PRODUCTNAME}.lnk"
    RMDir /r "$AppData\${LEGACY_EXECUTABLE}"
    # Locate the legacy install directory: InstallLocation under HKLM
    # (all-users install) first, then HKCU (per-user install). Installers
    # predating the InstallLocation write are recovered from the
    # UninstallString path so no legacy install variant is left behind.
    ReadRegStr $1 HKLM "${LEGACY_UNINST_KEY}" "InstallLocation"
    ${If} $1 == ""
        ReadRegStr $1 HKCU "${LEGACY_UNINST_KEY}" "InstallLocation"
    ${EndIf}
    ${If} $1 == ""
        ReadRegStr $2 HKLM "${LEGACY_UNINST_KEY}" "UninstallString"
        ${If} $2 == ""
            ReadRegStr $2 HKCU "${LEGACY_UNINST_KEY}" "UninstallString"
        ${EndIf}
        ${If} $2 != ""
            # UninstallString may be quoted ("C:\...\uninstall.exe"); strip
            # the surrounding quotes before taking the parent directory.
            StrCpy $3 $2 1
            ${If} $3 == '"'
                StrCpy $2 $2 "" 1
                StrCpy $2 $2 -1
            ${EndIf}
            ${GetParent} "$2" $1
        ${EndIf}
    ${EndIf}
    ${If} $1 != ""
        # Installer-based legacy install: drop its Apps & Features entries
        # (both hives — the old installer's shell context is unknown) and
        # remove its program directory. Guard: never remove a directory that
        # equals the new $INSTDIR (a user could have picked the old location
        # as the install target).
        DeleteRegKey HKLM "${LEGACY_UNINST_KEY}"
        DeleteRegKey HKCU "${LEGACY_UNINST_KEY}"
        ${If} $1 != "$INSTDIR"
            RMDir /r "$1"
        ${EndIf}
    ${Else}
        # Self-updated legacy install (no registry entry). If the default old
        # location still holds the legacy executable, remove that folder too —
        # but never the directory we just installed into.
        ${If} "$PROGRAMFILES64\${LEGACY_PRODUCTNAME}" != "$INSTDIR"
            IfFileExists "$PROGRAMFILES64\${LEGACY_PRODUCTNAME}\${LEGACY_EXECUTABLE}" 0 +2
                RMDir /r "$PROGRAMFILES64\${LEGACY_PRODUCTNAME}"
        ${EndIf}
    ${EndIf}
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath
    RMDir /r "$AppData\${LEGACY_EXECUTABLE}" # Legacy (pre-rename) WebView2 DataPath

    RMDir /r $INSTDIR

    # Remove shortcuts from BOTH shell contexts (current user and all users)
    # and for BOTH the new and legacy names. Wildcards also catch renamed or
    # duplicated variants (e.g. "svc (2).lnk"). The rename migration creates
    # shortcuts wherever legacy ones existed, so scan both contexts.
    SetShellVarContext current
    Delete "$DESKTOP\${INFO_PRODUCTNAME}*.lnk"
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}*.lnk"
    Delete "$DESKTOP\${LEGACY_PRODUCTNAME}*.lnk"
    Delete "$SMPROGRAMS\${LEGACY_PRODUCTNAME}*.lnk"
    SetShellVarContext all
    Delete "$DESKTOP\${INFO_PRODUCTNAME}*.lnk"
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}*.lnk"
    Delete "$DESKTOP\${LEGACY_PRODUCTNAME}*.lnk"
    Delete "$SMPROGRAMS\${LEGACY_PRODUCTNAME}*.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
