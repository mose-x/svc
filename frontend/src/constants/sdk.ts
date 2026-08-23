// Shared SDK metadata for the frontend. Centralizes the SDK color palette and
// category grouping so HomePage, Sidebar, and PathModal stay in sync. Keep this
// the single source of truth — do not re-declare these maps locally.
//
// Colors are brand/official hex values for each SDK's identity color. The
// `22` alpha suffix is appended at call sites (e.g. Sidebar chip backgrounds)
// rather than baked in here, so consumers can pick the opacity they need.

export const sdkColors: Record<string, string> = {
  nodejs: '#339933',
  jdk: '#f89820',
  go: '#00ADD8',
  python: '#3776AB',
  rust: '#CE422B',
  ruby: '#CC342D',
  dotnet: '#512BD4',
  php: '#777BB4',
  perl: '#39457E',
  maven: '#C71A36',
  gradle: '#02303A',
  flutter: '#02569B',
  android: '#3DDC84',
  dart: '#0175C2',
}

// Human-readable display names for each SDK type. Kept in sync with the
// sdkColors keys above — both maps cover all 14 SDKs. Consumers (PathModal,
// Sidebar, etc.) should use this instead of maintaining local name maps.
export const sdkDisplayNames: Record<string, string> = {
  nodejs: 'Node.js',
  jdk: 'JDK',
  go: 'Go',
  python: 'Python',
  rust: 'Rust',
  ruby: 'Ruby',
  dotnet: '.NET',
  php: 'PHP',
  perl: 'Perl',
  maven: 'Maven',
  gradle: 'Gradle',
  flutter: 'Flutter',
  android: 'Android',
  dart: 'Dart',
}

export interface SdkCategory {
  key: string
  sdkTypes: string[]
}

export const SDK_CATEGORIES: SdkCategory[] = [
  {
    key: 'runtime',
    sdkTypes: [
      'nodejs',
      'jdk',
      'go',
      'python',
      'rust',
      'ruby',
      'dotnet',
      'php',
      'perl',
    ],
  },
  { key: 'build', sdkTypes: ['maven', 'gradle'] },
  { key: 'mobile', sdkTypes: ['flutter', 'android', 'dart'] },
]

// Display names for external version managers detected on PATH. The backend
// reports machine names ("nvm-rust", "nvm"); the UI shows user-facing labels.
export const externalManagerNames: Record<string, string> = {
  'nvm-rust': 'nvm(nvm-rust)',
  nvm: 'nvm',
}

export const externalManagerName = (manager: string): string =>
  externalManagerNames[manager] || manager
