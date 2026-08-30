export type SdkType =
  | 'nodejs'
  | 'jdk'
  | 'go'
  | 'python'
  | 'rust'
  | 'ruby'
  | 'dotnet'
  | 'php'
  | 'perl'
  | 'maven'
  | 'gradle'
  | 'flutter'
  | 'android'
  | 'dart'

export interface PackageManagerInfo {
  name: string
  version: string
  installed: boolean
  parentSdk: string
}

export interface GlobalPackage {
  name: string
  version: string
}

export interface SdkStatus {
  sdkType: SdkType
  displayName: string
  configured: boolean
  pathConfigured: boolean // In PATH but not in .svc
  pathVersion: string // Version detected in PATH
  currentVersion: string
  installedVersions: string[]
  installPath: string
  needsSwitch: boolean // true if currentVersion is not in installedVersions
  systemProtected: boolean // system-managed copy in a protected OS path (cannot import)
  systemPath: string // resolved path of the system-protected copy
  externalManager: string // external version manager owning the PATH copy ("" = standalone)
  pathBinary: string // resolved absolute path of the PATH copy's binary
}

export interface VersionInfo {
  version: string
  major: number
  downloadUrl: string
  fileName: string
  isLts: boolean
  releaseDate: string
}

export interface InstallProgress {
  sdkType: SdkType
  version: string
  stage:
    | 'downloading'
    | 'extracting'
    | 'configuring_path'
    | 'verifying'
    | 'done'
    | 'error'
  percent: number
  message: string
  downloadedBytes: number
  totalBytes: number
  speedBytesPerSec: number
  downloadUrl: string
}

export interface ScanProgress {
  sdkType: SdkType
  displayName: string
  index: number
  total: number
}

export interface ProxySettings {
  enabled: boolean
  mode: 'system' | 'custom'
  url: string
  protocol: string
}
