import { useState, useEffect, useRef } from 'react'
import {
  Radio,
  Divider,
  Button,
  App,
  Tabs,
  Descriptions,
  Switch,
  Input,
  Modal,
  Progress,
  Space,
  Tooltip,
} from 'antd'
import {
  SettingOutlined,
  BgColorsOutlined,
  GlobalOutlined,
  SyncOutlined,
  InfoCircleOutlined,
  FileProtectOutlined,
  GithubOutlined,
  WifiOutlined,
  LinkOutlined,
  FolderOutlined,
  DeleteOutlined,
  FileTextOutlined,
  ReloadOutlined,
  UndoOutlined,
  KeyOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  GetSettings,
  SaveSettings,
  GetAppInfo,
  GetDefaultEndpoints,
  GetEndpoints,
  SaveEndpoints,
  SaveGithubToken,
  GetDefaultInstallPath,
  GetInstallPath,
  MigrateInstallPath,
  CheckUpdate,
  DownloadUpdate,
  ApplyUpdate,
  RollbackUpdate,
  HasUpdateBackup,
  GetTmpCacheSize,
  CleanTmpCache,
  CheckProxy,
  GetLogFiles,
  GetLogContent,
  CleanLogs,
  GetLogDir,
  DeleteLogFile,
} from '../../../wailsjs/go/main/App'
import { BrowserOpenURL, EventsOn } from '../../../wailsjs/runtime/runtime'
import { SDK_CATEGORIES } from '../../constants/sdk'
import { errMsg } from '../../utils/error'
import { formatBytes } from '../../utils/format'

interface AppSettings {
  theme: string
  language: string
  proxy: {
    enabled: boolean
    mode: string
    url: string
    protocol: string
  }
  // Mirrored from the backend config. Single-field handlers send the whole
  // settings object to SaveSettings, so this snapshot must keep the current
  // endpoints/installPath or a later save (e.g. theme change) would clobber
  // values that SaveEndpoints/MigrateInstallPath changed in the meantime.
  endpoints: Record<string, string>
  installPath: string
  githubMirror: string
  githubToken: string
  downloadThreads: number
}

interface AppInfo {
  version: string
  goVersion: string
  license: string
  repoUrl: string
}

interface EndpointInfo {
  sdkType: string
  displayName: string
  defaultEndpoint: string
}

interface UpdateInfo {
  hasUpdate: boolean
  latestVersion: string
  changelog: string
  downloadUrl: string
  filename: string
  sha256: string
}

interface SettingsPageProps {
  onBack: () => void
  onThemeChange: (theme: string) => void
  onLanguageChange: (lang: string) => void
}

const SettingsPage: React.FC<SettingsPageProps> = ({
  onThemeChange,
  onLanguageChange,
}) => {
  const { t, i18n } = useTranslation()
  const { message: msgApi } = App.useApp()
  const [modal, contextHolder] = Modal.useModal()
  const [settings, setSettings] = useState<AppSettings>({
    theme: 'dark',
    language: 'zh',
    proxy: { enabled: false, mode: 'system', url: '', protocol: 'http' },
    endpoints: {},
    installPath: '',
    githubMirror: '',
    githubToken: '',
    downloadThreads: 4,
  })
  const [appInfo, setAppInfo] = useState<AppInfo | null>(null)
  // Whether a rollback backup (.bak) exists. Fetched on mount so the rollback
  // button can be disabled instead of surfacing a "no backup found" error.
  const [hasBackup, setHasBackup] = useState(false)
  // The version reported by the backend at startup (from about.json). Unlike
  // appInfo.version, this is NOT mutated when a download completes, so the
  // "update ready" hint keeps showing the real current version instead of
  // collapsing to "v1.0.5 -> v1.0.5" after the new package is downloaded.
  const [originalVersion, setOriginalVersion] = useState<string>('')
  const [checking, setChecking] = useState(false)
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [updateModalOpen, setUpdateModalOpen] = useState(false)
  const [downloadProgress, setDownloadProgress] = useState<{
    percent: number
    message: string
    stage: string
  } | null>(null)
  const [downloading, setDownloading] = useState(false)
  const [downloadDone, setDownloadDone] = useState(false)
  const [defaultEndpoints, setDefaultEndpoints] = useState<EndpointInfo[]>([])
  const [customEndpoints, setCustomEndpoints] = useState<
    Record<string, string>
  >({})
  const [draftEndpoints, setDraftEndpoints] = useState<Record<string, string>>(
    {},
  )
  const [installPath, setInstallPath] = useState('')
  const [defaultInstallPath, setDefaultInstallPath] = useState('')
  const [installPathDraft, setInstallPathDraft] = useState('')
  const [migrating, setMigrating] = useState(false)
  const [tmpCacheSize, setTmpCacheSize] = useState(0)
  const [cleaning, setCleaning] = useState(false)
  const [checkingProxy, setCheckingProxy] = useState<Record<string, boolean>>(
    {},
  )
  const [logFiles, setLogFiles] = useState<any[]>([])
  const [logModalOpen, setLogModalOpen] = useState(false)
  const [currentLogFile, setCurrentLogFile] = useState('')
  const [logContent, setLogContent] = useState('')
  const [loadingLogs, setLoadingLogs] = useState(false)
  const [cleaningLogs, setCleaningLogs] = useState(false)
  const [logDir, setLogDir] = useState('')

  useEffect(() => {
    const off = EventsOn('update:progress', (progress: any) => {
      setDownloadProgress({
        percent: progress.percent,
        message: progress.message,
        stage: progress.stage,
      })
      if (progress.stage === 'done') {
        setDownloading(false)
        setDownloadDone(true)
        // Reflect the downloaded version immediately so the About panel does
        // not keep showing the old version until the user restarts. The
        // authoritative value is reloaded from about.json on next restart.
        setAppInfo((prev) =>
          prev
            ? { ...prev, version: updateInfo?.latestVersion || prev.version }
            : prev,
        )
      }
    })
    return () => {
      off()
    }
  }, [updateInfo])

  useEffect(() => {
    GetSettings()
      .then((s) => {
        if (s) setSettings(s)
      })
      .catch((e) => console.error('Failed to load settings:', e))
    GetAppInfo()
      .then((info) => {
        if (info) {
          setAppInfo(info)
          setOriginalVersion(info.version)
        }
      })
      .catch((e) => console.error('Failed to load app info:', e))
    HasUpdateBackup()
      .then(setHasBackup)
      .catch(() => setHasBackup(false))
    GetDefaultEndpoints()
      .then((de) => setDefaultEndpoints(de || []))
      .catch((e) => console.error('Failed to load default endpoints:', e))
    GetDefaultInstallPath()
      .then((p) => {
        if (p) {
          setDefaultInstallPath(p)
          setInstallPathDraft(p)
        }
      })
      .catch((e) => console.error('Failed to load default install path:', e))
    GetInstallPath()
      .then((p) => {
        if (p) {
          setInstallPath(p)
          setInstallPathDraft(p)
        }
      })
      .catch((e) => console.error('Failed to load install path:', e))
    GetEndpoints()
      .then((ce) => {
        const endpoints = ce || {}
        setCustomEndpoints(endpoints)
        setDraftEndpoints({ ...endpoints })
      })
      .catch((e) => console.error('Failed to load endpoints:', e))
    loadTmpCacheSize()
    loadLogFiles()
    GetLogDir()
      .then((d: string) => setLogDir(d || ''))
      .catch((e) => console.error('Failed to load log dir:', e))
  }, [])

  const loadTmpCacheSize = () => {
    GetTmpCacheSize()
      .then((size) => setTmpCacheSize(size || 0))
      .catch(() => {})
  }

  const loadLogFiles = () => {
    setLoadingLogs(true)
    GetLogFiles()
      .then((files: any[]) => {
        setLogFiles(files || [])
      })
      .catch(() => {})
      .finally(() => {
        setLoadingLogs(false)
      })
  }

  // Monotonic request sequence: when the user switches log files quickly, a
  // slow response for an earlier file must not overwrite the content of the
  // file currently being viewed.
  const logRequestSeq = useRef(0)

  const handleViewLog = async (filename: string) => {
    const seq = ++logRequestSeq.current
    setCurrentLogFile(filename)
    setLogContent('')
    setLogModalOpen(true)
    try {
      const content = await GetLogContent(filename)
      if (seq !== logRequestSeq.current) return // superseded by a newer view
      setLogContent(content || '')
    } catch (e: any) {
      if (seq !== logRequestSeq.current) return
      msgApi.error(errMsg(e))
    }
  }

  const handleCleanLogs = () => {
    modal.confirm({
      title: t('logs.cleanConfirm'),
      content: t('logs.cleanConfirmDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      okButtonProps: { danger: true },
      onOk: async () => {
        setCleaningLogs(true)
        try {
          await CleanLogs()
          msgApi.success(t('logs.cleanSuccess'))
          loadLogFiles()
        } catch (e: any) {
          msgApi.error(t('logs.cleanFail', { error: errMsg(e) }))
        } finally {
          setCleaningLogs(false)
        }
      },
    })
  }

  const handleDeleteLog = (filename: string) => {
    modal.confirm({
      title: t('logs.deleteConfirm'),
      content: t('logs.deleteConfirmDesc', { filename }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await DeleteLogFile(filename)
          msgApi.success(t('logs.deleteSuccess'))
          loadLogFiles()
        } catch (e: any) {
          msgApi.error(t('logs.deleteFail', { error: errMsg(e) }))
        }
      },
    })
  }

  const handleCheckProxy = async (target: string, label: string) => {
    // Guard: when the user picked "custom" proxy mode but left the URL
    // empty, a connectivity check would fall back to a direct connection
    // (BuildClient skips proxy setup on empty URL). In networks where the
    // target is only reachable via proxy (e.g. Google in CN), this hangs
    // for the full timeout and reports a misleading "connection failed".
    // Reject up front with a clear hint instead.
    if (
      settings.proxy?.enabled &&
      settings.proxy.mode === 'custom' &&
      !settings.proxy.url?.trim()
    ) {
      msgApi.warning(t('settings.proxyUrlRequired'))
      return
    }
    setCheckingProxy((prev) => ({ ...prev, [target]: true }))
    try {
      await CheckProxy(target)
      msgApi.success(t('settings.proxyCheckSuccess', { target: label }))
    } catch (e: any) {
      msgApi.error(
        t('settings.proxyCheckFail', { target: label, error: errMsg(e) }),
      )
    } finally {
      setCheckingProxy((prev) => ({ ...prev, [target]: false }))
    }
  }

  const handleCleanTmpCache = async () => {
    setCleaning(true)
    try {
      await CleanTmpCache()
      msgApi.success(t('settings.cleanSuccess'))
      loadTmpCacheSize()
    } catch (e: any) {
      msgApi.error(errMsg(e))
    } finally {
      setCleaning(false)
    }
  }

  // Single save path for every "change one setting" control: merge the
  // patch into the current settings, persist, then toast. afterSave runs
  // once the backend confirms the save (theme/language side effects hook in
  // here).
  const updateSettings = (
    patch: Partial<AppSettings>,
    afterSave?: () => void,
  ) => {
    const newSettings = { ...settings, ...patch } as any
    setSettings(newSettings)
    SaveSettings(newSettings)
      .then(() => {
        afterSave?.()
        msgApi.success(t('settings.settingsSaved'))
      })
      .catch((e: any) =>
        msgApi.error(t('settings.saveFail', { error: errMsg(e) })),
      )
  }

  const handleThemeChange = (theme: string) =>
    updateSettings({ theme }, () => onThemeChange(theme))

  const handleLanguageChange = (lang: string) =>
    updateSettings({ language: lang }, () => {
      i18n.changeLanguage(lang)
      onLanguageChange(lang)
    })

  const handleProxyToggle = (enabled: boolean) =>
    updateSettings({ proxy: { ...settings.proxy, enabled } })

  const handleProxyModeChange = (mode: string) =>
    updateSettings({ proxy: { ...settings.proxy, mode } })

  const handleProxyUrlChange = (url: string) =>
    updateSettings({ proxy: { ...settings.proxy, url } })

  const handleProxyProtocolChange = (protocol: string) =>
    updateSettings({ proxy: { ...settings.proxy, protocol } })

  const handleGithubMirrorChange = (url: string) =>
    updateSettings({ githubMirror: url })

  // GitHub Token: stored base64 on the backend, returned masked (first6***last6)
  // by GetSettings. The raw token is never held in frontend state -- the edit
  // flow uses a separate draft field and persists via SaveGithubToken, then
  // reloads settings so only the masked form is shown.
  const [tokenEditing, setTokenEditing] = useState(false)
  const [tokenDraft, setTokenDraft] = useState('')

  const handleSaveToken = () => {
    const val = tokenDraft.trim()
    if (!val) {
      msgApi.warning(t('settings.githubTokenEmpty'))
      return
    }
    SaveGithubToken(val)
      .then(() => {
        setTokenEditing(false)
        setTokenDraft('')
        return GetSettings()
      })
      .then((s) => {
        if (s) setSettings(s)
        msgApi.success(t('settings.githubTokenSaved'))
      })
      .catch((e: any) => {
        msgApi.error(t('settings.githubTokenSaveFail', { error: errMsg(e) }))
      })
  }

  const handleClearToken = () => {
    modal.confirm({
      title: t('settings.githubTokenClear'),
      content: t('settings.githubTokenClearDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      okButtonProps: { danger: true },
      onOk: () => {
        SaveGithubToken('')
          .then(() => {
            setTokenEditing(false)
            setTokenDraft('')
            return GetSettings()
          })
          .then((s) => {
            if (s) setSettings(s)
            msgApi.success(t('settings.githubTokenCleared'))
          })
          .catch((e: any) => {
            msgApi.error(errMsg(e))
          })
      },
    })
  }

  const handleDownloadThreadsChange = (threads: number) =>
    updateSettings({ downloadThreads: threads })

  const handleCheckUpdate = async () => {
    setChecking(true)
    setUpdateInfo(null)
    // Reset download state from any previous check/download cycle, so a
    // completed download doesn't keep showing "update ready" on a re-check.
    setDownloading(false)
    setDownloadProgress(null)
    setDownloadDone(false)
    try {
      const info = await CheckUpdate()
      if (info.hasUpdate) {
        setUpdateInfo(info)
        setUpdateModalOpen(true)
      } else {
        msgApi.success(t('about.upToDate'))
      }
    } catch (e: any) {
      msgApi.error(t('about.checkUpdateFail', { error: errMsg(e) }))
    } finally {
      setChecking(false)
    }
  }

  const handleRollback = async () => {
    // Race guard: re-check right before the confirm dialog so a backup that
    // disappeared since mount shows a friendly notice instead of an error.
    const available = await HasUpdateBackup().catch(() => false)
    if (!available) {
      setHasBackup(false)
      msgApi.warning(t('settings.rollbackNoBackup'))
      return
    }
    modal.confirm({
      title: t('settings.rollbackConfirm'),
      content: t('settings.rollbackConfirmDesc'),
      okText: t('settings.rollbackBtn'),
      cancelText: t('app.cancel'),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await RollbackUpdate()
        } catch (e: any) {
          msgApi.error(t('settings.rollbackFail', { error: errMsg(e) }))
        }
      },
    })
  }

  // Install path
  const hasInstallPathChange = () => {
    return installPathDraft.trim() !== installPath.trim()
  }

  const handleSaveInstallPath = () => {
    const newPath = installPathDraft.trim()
    if (!newPath) return
    modal.confirm({
      title: t('settings.installPathConfirm'),
      content: t('settings.installPathConfirmDesc', { path: newPath }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: async () => {
        setMigrating(true)
        try {
          await MigrateInstallPath(newPath)
          setInstallPath(newPath)
          // Keep the settings snapshot in sync (see AppSettings comment):
          // whole-object SaveSettings must not revert the migration.
          setSettings((prev) => ({ ...prev, installPath: newPath }))
          msgApi.success(t('settings.installPathSuccess'))
        } catch (e: any) {
          msgApi.error(t('settings.installPathFail', { error: errMsg(e) }))
        } finally {
          setMigrating(false)
        }
      },
    })
  }

  const handleResetInstallPath = () => {
    modal.confirm({
      title: t('settings.installPathResetConfirm'),
      content: t('settings.installPathResetConfirmDesc', {
        path: defaultInstallPath,
      }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: async () => {
        setMigrating(true)
        try {
          await MigrateInstallPath(defaultInstallPath)
          setInstallPath(defaultInstallPath)
          setInstallPathDraft(defaultInstallPath)
          // Keep the settings snapshot in sync (see handleSaveInstallPath).
          setSettings((prev) => ({ ...prev, installPath: defaultInstallPath }))
          msgApi.success(t('settings.installPathSuccess'))
        } catch (e: any) {
          msgApi.error(t('settings.installPathFail', { error: errMsg(e) }))
        } finally {
          setMigrating(false)
        }
      },
    })
  }

  const handleEndpointChange = (sdkType: string, value: string) => {
    setDraftEndpoints((prev) => ({ ...prev, [sdkType]: value }))
  }

  const hasEndpointChanges = () => {
    return JSON.stringify(draftEndpoints) !== JSON.stringify(customEndpoints)
  }

  const handleSaveEndpoints = () => {
    modal.confirm({
      title: t('endpoint.confirmSave'),
      content: t('endpoint.confirmSaveDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: () => {
        // Clean up empty values
        const cleaned: Record<string, string> = {}
        for (const [k, v] of Object.entries(draftEndpoints)) {
          if (v.trim()) cleaned[k] = v.trim()
        }
        SaveEndpoints(cleaned)
          .then(() => {
            setCustomEndpoints(cleaned)
            setDraftEndpoints({ ...cleaned })
            // Keep the settings snapshot in sync: single-field handlers send
            // the whole object to SaveSettings and would otherwise resurrect
            // stale endpoints.
            setSettings((prev) => ({ ...prev, endpoints: { ...cleaned } }))
            msgApi.success(t('settings.settingsSaved'))
          })
          .catch((e: any) =>
            msgApi.error(t('settings.saveFail', { error: errMsg(e) })),
          )
      },
    })
  }

  const handleResetOneEndpoint = (sdkType: string, displayName: string) => {
    modal.confirm({
      title: t('endpoint.confirmResetOne', { sdk: displayName }),
      content: t('endpoint.confirmResetOneDesc', { sdk: displayName }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: () => {
        const newDraft = { ...draftEndpoints }
        delete newDraft[sdkType]
        setDraftEndpoints(newDraft)
        // Save immediately
        const cleaned: Record<string, string> = {}
        for (const [k, v] of Object.entries(newDraft)) {
          if (v.trim()) cleaned[k] = v.trim()
        }
        SaveEndpoints(cleaned)
          .then(() => {
            setCustomEndpoints(cleaned)
            setDraftEndpoints({ ...cleaned })
            // Keep the settings snapshot in sync (see handleSaveEndpoints).
            setSettings((prev) => ({ ...prev, endpoints: { ...cleaned } }))
            msgApi.success(t('settings.settingsSaved'))
          })
          .catch((e: any) =>
            msgApi.error(t('settings.saveFail', { error: errMsg(e) })),
          )
      },
    })
  }

  const tabItems = [
    {
      key: 'settings',
      label: (
        <span>
          <SettingOutlined />
          {t('settings.title')}
        </span>
      ),
      children: (
        <div className="settings-content">
          {/* Theme */}
          <div className="settings-section">
            <div className="settings-label">
              <BgColorsOutlined style={{ marginRight: 8 }} />
              {t('settings.theme')}
            </div>
            <Radio.Group
              value={settings.theme}
              onChange={(e) => handleThemeChange(e.target.value)}
              optionType="button"
              buttonStyle="solid"
            >
              <Radio.Button value="system">
                {t('settings.themeSystem')}
              </Radio.Button>
              <Radio.Button value="dark">
                {t('settings.themeDark')}
              </Radio.Button>
              <Radio.Button value="light">
                {t('settings.themeLight')}
              </Radio.Button>
            </Radio.Group>
          </div>

          <Divider />

          {/* Language */}
          <div className="settings-section">
            <div className="settings-label">
              <GlobalOutlined style={{ marginRight: 8 }} />
              {t('settings.language')}
            </div>
            <Radio.Group
              value={settings.language}
              onChange={(e) => handleLanguageChange(e.target.value)}
              optionType="button"
              buttonStyle="solid"
            >
              <Radio.Button value="zh">Chinese</Radio.Button>
              <Radio.Button value="en">English</Radio.Button>
            </Radio.Group>
          </div>

          <Divider />

          {/* Proxy */}
          <div className="settings-section">
            <div
              className="settings-label"
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
              }}
            >
              <span>
                <WifiOutlined style={{ marginRight: 8 }} />
                {t('settings.proxy')}
              </span>
              <Switch
                checked={settings.proxy?.enabled}
                onChange={handleProxyToggle}
                size="small"
              />
            </div>
            {settings.proxy?.enabled && (
              <div style={{ marginTop: 12 }}>
                <Radio.Group
                  value={settings.proxy.mode}
                  onChange={(e) => handleProxyModeChange(e.target.value)}
                  optionType="button"
                  buttonStyle="solid"
                  style={{ marginBottom: 12 }}
                >
                  <Radio.Button value="system">
                    {t('settings.proxySystem')}
                  </Radio.Button>
                  <Radio.Button value="custom">
                    {t('settings.proxyCustom')}
                  </Radio.Button>
                </Radio.Group>
                {settings.proxy.mode === 'custom' && (
                  <div
                    style={{ display: 'flex', flexDirection: 'column', gap: 8 }}
                  >
                    <Radio.Group
                      value={settings.proxy.protocol || 'http'}
                      onChange={(e) =>
                        handleProxyProtocolChange(e.target.value)
                      }
                      optionType="button"
                      buttonStyle="solid"
                      size="small"
                    >
                      <Radio.Button value="http">HTTP</Radio.Button>
                      <Radio.Button value="socks5">SOCKS5</Radio.Button>
                    </Radio.Group>
                    <Input
                      placeholder={
                        settings.proxy.protocol === 'socks5'
                          ? '127.0.0.1:1080'
                          : '127.0.0.1:7890'
                      }
                      value={settings.proxy.url}
                      onChange={(e) =>
                        // M13: update local state only; persist on blur (see
                        // onBlur below) to avoid SaveSettings per keystroke.
                        setSettings({
                          ...settings,
                          proxy: { ...settings.proxy, url: e.target.value },
                        } as any)
                      }
                      onBlur={(e) =>
                        handleProxyUrlChange(e.target.value.trim())
                      }
                      style={{ maxWidth: 400 }}
                    />
                  </div>
                )}
                <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                  <Button
                    size="small"
                    loading={checkingProxy['https://www.baidu.com']}
                    onClick={() =>
                      handleCheckProxy(
                        'https://www.baidu.com',
                        t('settings.proxyCheckBaidu'),
                      )
                    }
                  >
                    {t('settings.proxyCheckBaidu')}
                  </Button>
                  <Button
                    size="small"
                    loading={checkingProxy['https://www.google.com']}
                    onClick={() =>
                      handleCheckProxy(
                        'https://www.google.com',
                        t('settings.proxyCheckGoogle'),
                      )
                    }
                  >
                    {t('settings.proxyCheckGoogle')}
                  </Button>
                </div>
              </div>
            )}
          </div>

          <Divider />

          {/* GitHub Mirror */}
          <div className="settings-section">
            <div className="settings-label">
              <GithubOutlined style={{ marginRight: 8 }} />
              {t('settings.githubMirror')}
            </div>
            <div style={{ paddingLeft: 0 }}>
              <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
                <Input
                  value={settings.githubMirror || ''}
                  onChange={(e) => {
                    const val = e.target.value
                    setSettings({ ...settings, githubMirror: val } as any)
                  }}
                  placeholder="https://ghfast.top"
                  style={{ flex: 1 }}
                />
                <Button
                  type="primary"
                  onClick={() => {
                    const trimmed = (settings.githubMirror || '').trim()
                    handleGithubMirrorChange(trimmed)
                  }}
                >
                  {t('app.confirm')}
                </Button>
              </div>
              <div style={{ fontSize: 12, color: '#888' }}>
                {t('settings.githubMirrorDesc')}
              </div>
            </div>
          </div>

          {/* GitHub Token */}
          <div className="settings-section">
            <div className="settings-label">
              <KeyOutlined style={{ marginRight: 8 }} />
              {t('settings.githubToken')}
            </div>
            <div style={{ paddingLeft: 0 }}>
              {tokenEditing ? (
                <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
                  <Input.Password
                    value={tokenDraft}
                    onChange={(e) => setTokenDraft(e.target.value)}
                    placeholder="ghp_xxxxxxxxxxxxxxxxxxxx"
                    style={{ flex: 1 }}
                    autoFocus
                    onPressEnter={handleSaveToken}
                  />
                  <Button type="primary" onClick={handleSaveToken}>
                    {t('app.confirm')}
                  </Button>
                  <Button
                    onClick={() => {
                      setTokenEditing(false)
                      setTokenDraft('')
                    }}
                  >
                    {t('app.cancel')}
                  </Button>
                </div>
              ) : (
                <div
                  style={{
                    display: 'flex',
                    gap: 8,
                    marginBottom: 8,
                    alignItems: 'center',
                  }}
                >
                  <span
                    style={{
                      flex: 1,
                      fontFamily: 'monospace',
                      color: settings.githubToken
                        ? 'var(--ant-color-text)'
                        : '#bbb',
                    }}
                  >
                    {settings.githubToken || t('settings.githubTokenNotSet')}
                  </span>
                  <Button
                    size="small"
                    icon={<ReloadOutlined />}
                    onClick={() => {
                      setTokenDraft('')
                      setTokenEditing(true)
                    }}
                  >
                    {settings.githubToken
                      ? t('settings.githubTokenEdit')
                      : t('settings.githubTokenSet')}
                  </Button>
                  {settings.githubToken && (
                    <Button
                      size="small"
                      danger
                      icon={<DeleteOutlined />}
                      onClick={handleClearToken}
                    >
                      {t('settings.githubTokenClear')}
                    </Button>
                  )}
                </div>
              )}
              <div style={{ fontSize: 12, color: '#888' }}>
                {t('settings.githubTokenDesc')}
              </div>
            </div>
          </div>

          <Divider />

          {/* Download Threads */}
          <div className="settings-section">
            <div className="settings-label">
              <SyncOutlined style={{ marginRight: 8 }} />
              {t('settings.downloadThreads')}
            </div>
            <Radio.Group
              value={settings.downloadThreads || 4}
              onChange={(e) => handleDownloadThreadsChange(e.target.value)}
              optionType="button"
              buttonStyle="solid"
            >
              <Radio.Button value={1}>1</Radio.Button>
              <Radio.Button value={2}>2</Radio.Button>
              <Radio.Button value={4}>4</Radio.Button>
              <Radio.Button value={8}>8</Radio.Button>
            </Radio.Group>
            <div style={{ fontSize: 12, color: '#888', marginTop: 8 }}>
              {t('settings.downloadThreadsDesc')}
            </div>
          </div>

          <Divider />

          {/* Install Path */}
          <div className="settings-section">
            <div className="settings-label">
              <FolderOutlined style={{ marginRight: 8 }} />
              {t('settings.installPath')}
            </div>
            <div style={{ paddingLeft: 0 }}>
              <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
                <Input
                  value={installPathDraft}
                  onChange={(e) => setInstallPathDraft(e.target.value)}
                  placeholder={defaultInstallPath}
                  style={{ flex: 1 }}
                />
                <Button
                  type="primary"
                  onClick={handleSaveInstallPath}
                  disabled={!hasInstallPathChange()}
                  loading={migrating}
                >
                  {t('app.confirm')}
                </Button>
                {installPath !== defaultInstallPath && (
                  <Button onClick={handleResetInstallPath} loading={migrating}>
                    {t('settings.installPathReset')}
                  </Button>
                )}
              </div>
              <div style={{ fontSize: 12, color: '#888' }}>
                {t('settings.installPathDefault')}: {defaultInstallPath}
              </div>
            </div>
          </div>

          <Divider />

          {/* Storage Management */}
          <div className="settings-section">
            <div className="settings-label">
              <DeleteOutlined style={{ marginRight: 8 }} />
              {t('settings.storageManagement')}
            </div>
            <div style={{ paddingLeft: 0 }}>
              {/* Tmp cache */}
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginBottom: 12,
                }}
              >
                <span style={{ fontSize: 13, color: '#aaa' }}>
                  {t('settings.tmpCache')}: {formatBytes(tmpCacheSize)}
                </span>
                <Button
                  danger
                  disabled={tmpCacheSize === 0}
                  loading={cleaning}
                  onClick={handleCleanTmpCache}
                >
                  {t('settings.clean')}
                </Button>
              </div>
            </div>
          </div>
        </div>
      ),
    },
    {
      key: 'endpoint',
      label: (
        <span>
          <LinkOutlined />
          {t('endpoint.title')}
        </span>
      ),
      children: (
        <div className="settings-content">
          <div
            style={{
              marginBottom: 16,
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
            }}
          >
            <span style={{ fontSize: 13, color: '#888' }}>
              {t('endpoint.description')}
            </span>
            <Button
              type="primary"
              size="small"
              onClick={handleSaveEndpoints}
              disabled={!hasEndpointChanges()}
            >
              {t('endpoint.save')}
            </Button>
          </div>
          {SDK_CATEGORIES.map((cat) => {
            const catEndpoints = defaultEndpoints.filter((ep) =>
              cat.sdkTypes.includes(ep.sdkType),
            )
            if (catEndpoints.length === 0) return null
            return (
              <div key={cat.key} style={{ marginBottom: 16 }}>
                <div
                  style={{
                    fontSize: 12,
                    color: '#666',
                    fontWeight: 600,
                    marginBottom: 8,
                    textTransform: 'uppercase',
                    letterSpacing: 1,
                  }}
                >
                  {t(`categories.${cat.key}`)}
                </div>
                {catEndpoints.map((ep) => {
                  const hasCustom = !!(
                    draftEndpoints[ep.sdkType] &&
                    draftEndpoints[ep.sdkType].trim()
                  )
                  return (
                    <div
                      key={ep.sdkType}
                      style={{ marginBottom: 12, paddingLeft: 16 }}
                    >
                      <div
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          marginBottom: 4,
                        }}
                      >
                        <span style={{ fontWeight: 500, minWidth: 100 }}>
                          {ep.displayName}
                        </span>
                        <span
                          style={{ fontSize: 12, color: '#888', marginLeft: 8 }}
                        >
                          {ep.defaultEndpoint}
                        </span>
                        {hasCustom && (
                          <Button
                            type="link"
                            size="small"
                            danger
                            style={{
                              marginLeft: 8,
                              padding: '0 4px',
                              fontSize: 12,
                            }}
                            onClick={() =>
                              handleResetOneEndpoint(ep.sdkType, ep.displayName)
                            }
                          >
                            {t('endpoint.resetDefault')}
                          </Button>
                        )}
                      </div>
                      <Input
                        size="small"
                        placeholder={ep.defaultEndpoint}
                        value={draftEndpoints[ep.sdkType] || ''}
                        onChange={(e) =>
                          handleEndpointChange(ep.sdkType, e.target.value)
                        }
                        allowClear
                        style={{ maxWidth: 500 }}
                      />
                    </div>
                  )
                })}
              </div>
            )
          })}
        </div>
      ),
    },
    {
      key: 'logs',
      label: (
        <span>
          <FileTextOutlined />
          {t('logs.title')}
        </span>
      ),
      children: (
        <div className="settings-content">
          <div className="settings-section">
            <div
              className="settings-label"
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
              }}
            >
              <span>
                <FileTextOutlined style={{ marginRight: 8 }} />
                {t('logs.logFiles')}
              </span>
              <div style={{ display: 'flex', gap: 8 }}>
                <Button
                  size="small"
                  icon={<ReloadOutlined />}
                  onClick={loadLogFiles}
                  loading={loadingLogs}
                >
                  {t('logs.refresh')}
                </Button>
                <Button
                  size="small"
                  danger
                  onClick={handleCleanLogs}
                  loading={cleaningLogs}
                  disabled={logFiles.length === 0}
                >
                  {t('logs.clean')}
                </Button>
              </div>
            </div>
            <div
              style={{
                marginTop: 12,
                fontSize: 12,
                color: 'var(--ant-color-text-secondary)',
                marginBottom: 12,
              }}
            >
              {t('logs.logDir')}: {logDir}
            </div>
            {loadingLogs ? (
              <div
                style={{
                  textAlign: 'center',
                  padding: '40px 0',
                  color: 'var(--ant-color-text-secondary)',
                }}
              >
                {t('settings.loading')}
              </div>
            ) : logFiles.length === 0 ? (
              <div
                style={{
                  textAlign: 'center',
                  padding: '40px 0',
                  color: 'var(--ant-color-text-secondary)',
                }}
              >
                {t('logs.noLogs')}
              </div>
            ) : (
              <div
                style={{
                  border: '1px solid var(--ant-color-border)',
                  borderRadius: 8,
                  overflow: 'hidden',
                }}
              >
                <div
                  style={{
                    display: 'flex',
                    padding: '8px 12px',
                    background: 'var(--ant-color-bg-layout)',
                    fontSize: 12,
                    fontWeight: 600,
                    color: 'var(--ant-color-text-secondary)',
                  }}
                >
                  <div style={{ flex: 2 }}>{t('settings.filename')}</div>
                  <div style={{ flex: 1, textAlign: 'right' }}>
                    {t('logs.size')}
                  </div>
                  <div style={{ flex: 2, textAlign: 'right', paddingRight: 8 }}>
                    {t('logs.modified')}
                  </div>
                  <div style={{ width: 120, textAlign: 'right' }}>
                    {t('logs.actions')}
                  </div>
                </div>
                {logFiles.map((file: any, idx: number) => (
                  <div
                    key={file?.name || idx}
                    style={{
                      display: 'flex',
                      padding: '8px 12px',
                      borderTop:
                        idx > 0 ? '1px solid var(--ant-color-border)' : 'none',
                      alignItems: 'center',
                      fontSize: 13,
                      color: 'var(--ant-color-text)',
                    }}
                  >
                    <div style={{ flex: 2, fontFamily: 'monospace' }}>
                      {file.name}
                    </div>
                    <div
                      style={{
                        flex: 1,
                        textAlign: 'right',
                        color: 'var(--ant-color-text-secondary)',
                      }}
                    >
                      {formatBytes(file.size)}
                    </div>
                    <div
                      style={{
                        flex: 2,
                        textAlign: 'right',
                        paddingRight: 8,
                        color: 'var(--ant-color-text-secondary)',
                      }}
                    >
                      {file.modTime}
                    </div>
                    <div
                      style={{
                        width: 120,
                        textAlign: 'right',
                        display: 'flex',
                        gap: 8,
                        justifyContent: 'flex-end',
                      }}
                    >
                      <Button
                        type="link"
                        size="small"
                        onClick={() => handleViewLog(file.name)}
                      >
                        {t('logs.view')}
                      </Button>
                      <Button
                        type="link"
                        size="small"
                        danger
                        onClick={() => handleDeleteLog(file.name)}
                      >
                        {t('logs.delete')}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      ),
    },
    {
      key: 'about',
      label: (
        <span>
          <InfoCircleOutlined />
          {t('about.title')}
        </span>
      ),
      children: (
        <div className="settings-content">
          {appInfo && (
            <>
              {/* Version Info */}
              <div className="settings-section">
                <div className="settings-label">
                  <InfoCircleOutlined style={{ marginRight: 8 }} />
                  {t('about.version')}
                </div>
                <Descriptions
                  column={1}
                  size="small"
                  bordered
                  style={{ maxWidth: 400 }}
                >
                  <Descriptions.Item label={t('about.version')}>
                    v{appInfo.version}
                  </Descriptions.Item>
                  <Descriptions.Item label={t('about.goVersion')}>
                    {appInfo.goVersion}
                  </Descriptions.Item>
                </Descriptions>
              </div>

              <Divider />

              {/* Check for Updates */}
              <div className="settings-section">
                <div className="settings-label">
                  <SyncOutlined style={{ marginRight: 8 }} />
                  {t('about.checkUpdate')}
                </div>
                <Space>
                  <Button
                    icon={<SyncOutlined spin={checking} />}
                    onClick={handleCheckUpdate}
                    loading={checking}
                  >
                    {t('about.checkUpdateBtn')}
                  </Button>
                  <Tooltip
                    title={!hasBackup ? t('settings.rollbackNoBackup') : ''}
                  >
                    <Button
                      icon={<UndoOutlined />}
                      onClick={handleRollback}
                      disabled={!hasBackup}
                    >
                      {t('settings.rollbackBtn')}
                    </Button>
                  </Tooltip>
                </Space>
              </div>

              <Divider />

              {/* License */}
              <div className="settings-section">
                <div className="settings-label">
                  <FileProtectOutlined style={{ marginRight: 8 }} />
                  {t('about.license')}
                </div>
                <span style={{ color: '#aaa' }}>{appInfo.license}</span>
              </div>

              <Divider />

              {/* Repo */}
              <div className="settings-section">
                <div className="settings-label">
                  <GithubOutlined style={{ marginRight: 8 }} />
                  {t('about.repo')}
                </div>
                <a
                  href="#"
                  onClick={(e) => {
                    e.preventDefault()
                    BrowserOpenURL(appInfo.repoUrl)
                  }}
                  style={{ color: '#1677ff' }}
                >
                  {appInfo.repoUrl}
                </a>
              </div>
            </>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="settings-page">
      {contextHolder}
      <div className="settings-header">
        <Tabs items={tabItems} style={{ flex: 1 }} />
      </div>

      <Modal
        title={
          downloadDone
            ? t('about.updateReady')
            : t('about.newVersion', {
                version: updateInfo?.latestVersion || '',
              })
        }
        open={updateModalOpen}
        onCancel={() => !downloading && setUpdateModalOpen(false)}
        closable={!downloading}
        maskClosable={!downloading}
        footer={
          downloadDone
            ? [
                <Button key="cancel" onClick={() => setUpdateModalOpen(false)}>
                  {t('about.updateLater')}
                </Button>,
                <Button
                  key="restart"
                  type="primary"
                  onClick={async () => {
                    try {
                      await ApplyUpdate()
                    } catch (e: any) {
                      msgApi.error(
                        t('about.applyUpdateFail', { error: errMsg(e) }),
                      )
                    }
                  }}
                >
                  {t('about.restartNow')}
                </Button>,
              ]
            : downloading
              ? [
                  <Button key="cancel" disabled>
                    {t('about.downloading')}
                  </Button>,
                ]
              : [
                  <Button
                    key="cancel"
                    onClick={() => setUpdateModalOpen(false)}
                  >
                    {t('app.cancel')}
                  </Button>,
                  <Button
                    key="download"
                    type="primary"
                    onClick={async () => {
                      if (!updateInfo?.downloadUrl) {
                        const releasesUrl = appInfo?.repoUrl
                          ? `${appInfo.repoUrl}/releases`
                          : ''
                        if (releasesUrl) {
                          BrowserOpenURL(releasesUrl)
                        }
                        setUpdateModalOpen(false)
                        return
                      }
                      setDownloading(true)
                      setDownloadProgress(null)
                      setDownloadDone(false)
                      try {
                        await DownloadUpdate(
                          updateInfo.downloadUrl,
                          updateInfo.sha256 || '',
                        )
                      } catch (e: any) {
                        setDownloading(false)
                        msgApi.error(
                          t('about.downloadFail', { error: errMsg(e) }),
                        )
                      }
                    }}
                  >
                    {updateInfo?.downloadUrl
                      ? t('about.downloadAndInstall')
                      : t('about.goDownload')}
                  </Button>,
                ]
        }
      >
        <div style={{ marginBottom: 8, fontSize: 13, color: '#888' }}>
          {t('about.currentVersion', {
            version: originalVersion || appInfo?.version || '',
          })}{' '}
          → v{updateInfo?.latestVersion}
        </div>

        {downloading && downloadProgress && (
          <div style={{ marginBottom: 12 }}>
            <Progress percent={downloadProgress.percent} status="active" />
            <div style={{ fontSize: 12, color: '#888', marginTop: 4 }}>
              {downloadProgress.stage === 'done'
                ? t('progress.updateDone')
                : downloadProgress.stage === 'downloading'
                  ? t('progress.updateDownloading')
                  : downloadProgress.stage === 'verifying'
                    ? t('progress.verifying')
                    : downloadProgress.message}
            </div>
          </div>
        )}

        {downloadDone && (
          <div
            style={{
              padding: '12px 16px',
              background: '#52c41a22',
              borderRadius: 8,
              marginBottom: 12,
              color: '#52c41a',
              fontSize: 13,
            }}
          >
            {t('about.updateReadyDesc')}
          </div>
        )}

        {updateInfo?.changelog && !downloading && (
          <div
            style={{
              background: 'var(--ant-color-bg-layout, #1a1a1a)',
              padding: '12px 16px',
              borderRadius: 8,
              fontSize: 13,
              lineHeight: 1.8,
              whiteSpace: 'pre-wrap',
              maxHeight: 300,
              overflowY: 'auto',
            }}
          >
            {updateInfo.changelog}
          </div>
        )}
      </Modal>

      <Modal
        title={t('logs.viewLog', { name: currentLogFile })}
        open={logModalOpen}
        onCancel={() => setLogModalOpen(false)}
        footer={[
          <Button key="close" onClick={() => setLogModalOpen(false)}>
            {t('logs.close')}
          </Button>,
        ]}
        width={800}
      >
        <div
          style={{
            background: '#000',
            color: '#0f0',
            padding: '12px 16px',
            borderRadius: 8,
            fontSize: 12,
            fontFamily: 'monospace',
            whiteSpace: 'pre-wrap',
            maxHeight: 500,
            overflowY: 'auto',
            lineHeight: 1.6,
          }}
        >
          {logContent || t('settings.noContent')}
        </div>
      </Modal>
    </div>
  )
}

export default SettingsPage
