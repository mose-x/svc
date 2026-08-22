import { useState, useEffect, useCallback, useMemo } from 'react'
import { ConfigProvider, theme, App as AntApp, Spin } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import enUS from 'antd/locale/en_US'
import { useTranslation } from 'react-i18next'
import Sidebar from './components/Sidebar/Sidebar'
import DetailPanel from './components/Panel/DetailPanel'
import HomePage from './components/HomePage/HomePage'
import PathModal from './components/PathModal/PathModal'
import SettingsPage from './components/Settings/SettingsPage'
import { SdkStatus, SdkType, InstallProgress } from './types/sdk'
import {
  GetAllSdkStatus,
  GetSettings,
  GetAppInfo,
} from '../wailsjs/go/main/App'
import {
  EventsOn,
  WindowSetLightTheme,
  WindowSetDarkTheme,
} from '../wailsjs/runtime/runtime'
import './i18n'
import './App.css'
import logoImg from './assets/logo.png'

function App() {
  const { i18n, t } = useTranslation()
  const [sdkStatuses, setSdkStatuses] = useState<SdkStatus[]>([])
  const [selectedSdk, setSelectedSdk] = useState<SdkType | null>(null)
  const [installProgressMap, setInstallProgressMap] = useState<
    Record<string, InstallProgress>
  >({})

  const downloadingSdks = useMemo(() => {
    const set = new Set<string>()
    for (const [key, p] of Object.entries(installProgressMap)) {
      if (p.stage !== 'done' && p.stage !== 'error') set.add(key)
    }
    return set
  }, [installProgressMap])
  const [themeMode, setThemeMode] = useState<string>('dark')
  const [language, setLanguage] = useState<string>('zh')
  const [showSettings, setShowSettings] = useState(false)
  const [showPathModal, setShowPathModal] = useState(false)
  const [initialLoading, setInitialLoading] = useState(true)
  const [minBootDone, setMinBootDone] = useState(false)
  const [appVersion, setAppVersion] = useState('')

  // Load settings + app version on mount
  useEffect(() => {
    GetSettings()
      .then((s) => {
        if (s) {
          setThemeMode(s.theme || 'dark')
          const lang = s.language || 'zh'
          setLanguage(lang)
          i18n.changeLanguage(lang)
        }
      })
      .catch((e) => console.error('Failed to load settings:', e))
    GetAppInfo()
      .then((info) => {
        if (!info) return
        setAppVersion(info.version)
        const link = document.querySelector(
          "link[rel='icon']",
        ) as HTMLLinkElement | null
        if (link) {
          link.href = `${logoImg}?v=${info.version}`
        }
      })
      .catch((e) => console.error('Failed to load app info:', e))
  }, [])

  useEffect(() => {
    // The inline boot screen in index.html covers the WebView's first paint;
    // drop it now that React has rendered so the app takes over seamlessly.
    document.getElementById('boot')?.remove()

    // Keep the loading state up for a minimum of MIN_LOADING_MS so startup
    // reads as a steady, deliberate loading screen instead of a brief flash.
    const MIN_LOADING_MS = 800
    const t = setTimeout(() => setMinBootDone(true), MIN_LOADING_MS)
    return () => clearTimeout(t)
  }, [])

  const refreshStatuses = useCallback(async () => {
    try {
      const statuses = await GetAllSdkStatus()
      // Cast from Wails-generated sdk.SdkStatus (sdkType: string) to the
      // frontend SdkStatus (sdkType: SdkType) — the runtime values always
      // carry valid SdkType strings; the Wails models just aren't narrowed.
      setSdkStatuses((statuses || []) as SdkStatus[])
    } catch (e) {
      console.error('Failed to get SDK status:', e)
    }
  }, [])

  useEffect(() => {
    refreshStatuses().finally(() => setInitialLoading(false))
  }, [refreshStatuses])

  useEffect(() => {
    // M3: Track timers per-sdkType so multiple done events don't overwrite
    // each other's timer ref, causing a leaked setTimeout.
    const timers = new Map<string, ReturnType<typeof setTimeout>>()
    const off = EventsOn('install:progress', (progress: InstallProgress) => {
      setInstallProgressMap((prev) => ({
        ...prev,
        [progress.sdkType]: progress,
      }))
      if (progress.stage === 'done' || progress.stage === 'error') {
        // Clear any existing timer for this SDK before setting a new one
        const existing = timers.get(progress.sdkType)
        if (existing) clearTimeout(existing)
        const timer = setTimeout(() => {
          refreshStatuses()
          setInstallProgressMap((prev) => {
            const cur = prev[progress.sdkType]
            // A new install for the same SDK may have started within the 2s
            // window; its entry is no longer done/error and must be kept.
            if (cur && cur.stage !== 'done' && cur.stage !== 'error') {
              return prev
            }
            const next = { ...prev }
            delete next[progress.sdkType]
            return next
          })
          timers.delete(progress.sdkType)
        }, 2000)
        timers.set(progress.sdkType, timer)
      }
    })
    return () => {
      off()
      timers.forEach((timer) => clearTimeout(timer))
    }
  }, [refreshStatuses])

  // System theme detection — reactive: updates when OS dark/light changes
  const [systemDark, setSystemDark] = useState(() => {
    if (typeof window !== 'undefined' && window.matchMedia) {
      return window.matchMedia('(prefers-color-scheme: dark)').matches
    }
    return true
  })

  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const handler = (e: MediaQueryListEvent) => setSystemDark(e.matches)
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  const isDark = themeMode === 'system' ? systemDark : themeMode === 'dark'

  // Sync window title bar theme with app theme
  useEffect(() => {
    if (isDark) {
      WindowSetDarkTheme()
    } else {
      WindowSetLightTheme()
    }
  }, [isDark])

  const antLocale = language === 'zh' ? zhCN : enUS

  const currentStatus = selectedSdk
    ? sdkStatuses.find((s) => s.sdkType === selectedSdk)
    : undefined

  const handleSelectSdk = (sdk: SdkType) => {
    setSelectedSdk(sdk)
    setShowSettings(false)
  }

  return (
    <ConfigProvider
      locale={antLocale}
      theme={{
        cssVar: true,
        algorithm: isDark ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: {
          colorPrimary: '#1677ff',
        },
        components: {
          Tooltip: {
            colorBgSpotlight: isDark ? 'rgba(0,0,0,0.85)' : '#fff',
            colorTextLightSolid: isDark ? '#fff' : 'rgba(0,0,0,0.88)',
          },
        },
      }}
    >
      <AntApp>
        {initialLoading || !minBootDone ? (
          <div
            className={`app-container ${isDark ? 'dark' : 'light'}`}
            style={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              height: '100vh',
              gap: 16,
            }}
          >
            <img
              src={appVersion ? `${logoImg}?v=${appVersion}` : logoImg}
              alt="logo"
              style={{ width: 120, height: 120 }}
            />
            <Spin size="large" />
            <div style={{ fontSize: 15, color: isDark ? '#aaa' : '#666' }}>
              {t('sidebar.loadingSdk')}
            </div>
          </div>
        ) : (
          <div className={`app-container ${isDark ? 'dark' : 'light'}`}>
            <Sidebar
              statuses={sdkStatuses}
              selectedSdk={showSettings ? null : selectedSdk}
              downloadingSdks={downloadingSdks}
              appVersion={appVersion}
              onSelect={handleSelectSdk}
              onGoHome={() => {
                setSelectedSdk(null)
                setShowSettings(false)
              }}
              onOpenSettings={() => setShowSettings(true)}
            />
            {showSettings ? (
              <SettingsPage
                onBack={() => setShowSettings(false)}
                onThemeChange={setThemeMode}
                onLanguageChange={setLanguage}
              />
            ) : selectedSdk ? (
              <DetailPanel
                key={selectedSdk}
                status={currentStatus}
                isDownloading={
                  currentStatus
                    ? downloadingSdks.has(currentStatus.sdkType)
                    : false
                }
                installProgress={
                  currentStatus
                    ? installProgressMap[currentStatus.sdkType] || null
                    : null
                }
                onRefresh={refreshStatuses}
              />
            ) : (
              <HomePage
                statuses={sdkStatuses}
                appVersion={appVersion}
                onSelect={handleSelectSdk}
                onOpenPathInfo={() => setShowPathModal(true)}
              />
            )}
            <PathModal
              open={showPathModal}
              onClose={() => setShowPathModal(false)}
              onRefresh={refreshStatuses}
            />
          </div>
        )}
      </AntApp>
    </ConfigProvider>
  )
}

export default App
