import { useState, useEffect, useCallback, useRef } from 'react'
import {
  Button,
  Input,
  Tag,
  Spin,
  Progress,
  App,
  Tooltip,
  Modal,
  Dropdown,
  Result,
  Typography,
} from 'antd'
import {
  CheckCircleFilled,
  CloseCircleFilled,
  WarningFilled,
  DeleteOutlined,
  DownloadOutlined,
  SearchOutlined,
  ReloadOutlined,
  CloudUploadOutlined,
  CopyOutlined,
  ImportOutlined,
  FolderOpenOutlined,
  FileOutlined,
  WarningOutlined,
  ExclamationCircleFilled,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  SdkStatus,
  VersionInfo,
  InstallProgress,
  PackageManagerInfo,
} from '../../types/sdk'
import {
  GetRemoteVersions,
  InstallSdk,
  GetPackageManagers,
  InstallPackageManager,
  UpdatePackageManager,
  SwitchVersion,
  SelectLocalFile,
  SelectLocalDir,
  ImportLocalSdk,
  ImportPathSdk,
  GetSdkDownloadURL,
  CheckSystemConflicts,
  UninstallVersion,
  CancelInstall,
} from '../../../wailsjs/go/main/App'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { formatBytes } from '../../utils/format'
import { externalManagerName } from '../../constants/sdk'
import { confirmAction } from '../../utils/confirmAction'
import { errMsg } from '../../utils/error'

interface DetailPanelProps {
  status: SdkStatus | undefined
  // True when the App-level progress map shows an in-flight install for this
  // SDK. Local `installing` state is lost when the panel remounts (keyed by
  // sdkType), so this prop is the authoritative guard against double starts.
  isDownloading: boolean
  installProgress: InstallProgress | null
  onRefresh: () => void
}

const DetailPanel: React.FC<DetailPanelProps> = ({
  status,
  isDownloading,
  installProgress,
  onRefresh,
}) => {
  const [versions, setVersions] = useState<VersionInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [loadErrorMessage, setLoadErrorMessage] = useState<string>('')
  const [searchText, setSearchText] = useState('')
  const [installing, setInstalling] = useState(false)
  const [packageManagers, setPackageManagers] = useState<PackageManagerInfo[]>(
    [],
  )
  const [pmLoading, setPmLoading] = useState<string>('')
  const [switching, setSwitching] = useState(false)
  const [importing, setImporting] = useState(false)
  const [conflicts, setConflicts] = useState<string[]>([])
  const { message: msgApi } = App.useApp()
  const { t } = useTranslation()
  const [modal, modalContextHolder] = Modal.useModal()

  const translateProgress = (progress: InstallProgress): string => {
    switch (progress.stage) {
      case 'downloading':
        if (progress.totalBytes > 0) {
          const percent = Math.floor(
            (progress.downloadedBytes * 100) / progress.totalBytes,
          )
          return t('progress.downloadingPercent', { percent })
        }
        return t('progress.downloading')
      case 'extracting':
        return t('progress.extracting')
      case 'verifying':
        return t('progress.verifying')
      case 'configuring_path':
        return t('progress.configuring_path')
      case 'done':
        return t('progress.done')
      case 'error':
        return t('progress.error', { error: progress.message })
      default:
        return progress.message
    }
  }

  const fetchVersions = useCallback(
    async (stale?: () => boolean) => {
      if (!status) return
      setLoading(true)
      setLoadError(false)
      setLoadErrorMessage('')
      try {
        const result = await GetRemoteVersions(status.sdkType)
        if (stale?.()) return
        // Defensive dedup by version string: a duplicated payload (from a
        // backend retry race or cache corruption) must never produce duplicate
        // rows in the list. First occurrence wins.
        const seen = new Set<string>()
        const deduped = (result || []).filter((v) => {
          if (seen.has(v.version)) return false
          seen.add(v.version)
          return true
        })
        setVersions(deduped)
      } catch (e: any) {
        if (stale?.()) return
        console.error('Failed to get remote versions:', e)
        // Preserve the backend error reason so the UI can show what actually
        // failed (GitHub API 403 rate limit, proxy/network error, decode
        // error, etc.) instead of a generic "failed to load" message.
        const reason = errMsg(e)
        setLoadErrorMessage(reason ? String(reason) : '')
        setLoadError(true)
      } finally {
        if (!stale?.()) setLoading(false)
      }
    },
    // Depend only on sdkType: App re-creates the status object on every
    // refresh, and the only field consumed here is status.sdkType.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [status?.sdkType],
  )

  const fetchPackageManagers = useCallback(
    async (stale?: () => boolean) => {
      if (!status) return
      try {
        const pms = await GetPackageManagers(status.sdkType)
        if (stale?.()) return
        setPackageManagers(pms || [])
      } catch {
        if (!stale?.()) setPackageManagers([])
      }
    },
    // Depend only on sdkType (see fetchVersions comment).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [status?.sdkType],
  )

  useEffect(() => {
    let stale = false
    if (status) {
      // Intentional synchronous reset when switching SDK: clear the previous
      // SDK's list/error/search state before the new fetch lands.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setVersions([])
      setLoadError(false)
      setSearchText('')
      fetchVersions(() => stale)
      fetchPackageManagers(() => stale)
    }
    return () => {
      stale = true
    }
    // Key on sdkType only: `status` gets a fresh object identity on every
    // App refresh, which used to re-trigger this effect each refresh —
    // re-fetching versions and wiping the user's search text. The panel is
    // already keyed by sdkType in App, and both callbacks are keyed the same
    // way, so sdkType is the only real dependency here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.sdkType])

  // Track the currently-selected SDK type in a ref so the event listener (which
  // is registered ONCE, see below) always reads the latest value instead of a
  // stale closure capture. Without this, rapidly switching SDKs leaves old
  // listeners alive (Wails off() + React.StrictMode double-invoke), and their
  // stale `status` closure makes them setVersions() a *different* SDK's version
  // list into the current panel -- e.g. rust's 1.66.1 leaking into the python
  // panel. Reading from a ref guarantees the filter always reflects the SDK the
  // user is actually looking at right now.
  const sdkTypeRef = useRef<string>('')
  useEffect(() => {
    sdkTypeRef.current = status?.sdkType || ''
  }, [status])

  // Silent background refresh listener. Registered ONCE on mount (empty deps)
  // -- NOT re-registered on every status change -- so there is never more than
  // one listener and no stale-closure window. The sdkType filter uses the ref
  // above so it always reflects the current panel.
  useEffect(() => {
    const off = EventsOn(
      'install:versions-refreshed',
      (payload: { sdkType: string; versions: VersionInfo[] }) => {
        if (!payload || !sdkTypeRef.current) return
        if (payload.sdkType !== sdkTypeRef.current) return
        setVersions((prev) => {
          // Only silently replace when we already have something to show; if
          // the initial fetch is still in flight (versions empty), let the
          // normal fetch path resolve so loading state stays consistent.
          if (prev.length === 0) return prev
          const fresh = payload.versions || []
          // Defensive dedup by version string: a stale or duplicated payload
          // must never produce duplicate rows in the list.
          const seen = new Set<string>()
          return fresh.filter((v) => {
            if (seen.has(v.version)) return false
            seen.add(v.version)
            return true
          })
        })
      },
    )
    return () => {
      off()
    }
  }, [])

  useEffect(() => {
    if (!status) return
    let stale = false
    // Intentional synchronous reset when switching SDK (see fetchVersions
    // effect).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setConflicts([])
    CheckSystemConflicts(status.sdkType)
      .then((entries) => {
        if (stale) return
        if (entries && entries.length > 0) {
          setConflicts(entries)
          modal.warning({
            title: t('detail.systemConflictTitle'),
            content: (
              <div>
                <p>{t('detail.systemConflictMsg')}</p>
                <ul
                  style={{
                    maxHeight: 200,
                    overflow: 'auto',
                    paddingLeft: 20,
                    margin: '8px 0',
                  }}
                >
                  {entries.map((e: string, i: number) => (
                    <li
                      key={i}
                      style={{
                        fontSize: 12,
                        color: '#666',
                        wordBreak: 'break-all',
                      }}
                    >
                      {e}
                    </li>
                  ))}
                </ul>
              </div>
            ),
            okText: t('app.confirm'),
            width: 520,
          })
        }
      })
      .catch(() => {})
    return () => {
      stale = true
    }
    // Key on sdkType only: `status` gets a fresh object identity on every
    // App refresh, which previously re-ran the check and re-popped the
    // conflict warning modal on each refresh. The panel is keyed by sdkType
    // in App, so one check per SDK selection is the intended behavior.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.sdkType])

  // Shared install flow without any confirm modal, so both entry points
  // (fresh install / reinstall) can wrap it in exactly one confirm dialog.
  const doInstall = (version: string) => {
    if (!status) return
    const sdkType = status.sdkType
    setInstalling(true)
    InstallSdk(sdkType, version)
      .then(() => {
        msgApi.success(
          t('detail.installSuccess', { sdk: status.displayName, version }),
        )
        onRefresh()
        fetchPackageManagers()
      })
      .catch((e: any) => {
        msgApi.error(t('detail.installFail', { error: errMsg(e) }))
      })
      .finally(() => {
        setInstalling(false)
      })
  }

  const handleInstall = (version: string) => {
    if (!status) return
    // App-level progress already in flight for this SDK (local `installing`
    // state may have been lost to a key-based remount) — block duplicates.
    if (isDownloading) return
    modal.confirm({
      title: t('detail.confirmInstallSdk', {
        sdk: status.displayName,
        version,
      }),
      content: status.externalManager ? (
        <div>
          <p>{t('detail.confirmInstallDesc', { version })}</p>
          <p style={{ color: '#faad14' }}>
            {t('detail.installExternalManagerWarn', {
              sdk: status.displayName,
              manager: externalManagerName(status.externalManager),
            })}
          </p>
        </div>
      ) : (
        t('detail.confirmInstallDesc', { version })
      ),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      maskClosable: false,
      onOk: () => doInstall(version),
    })
  }

  const handleReinstall = (version: string) => {
    if (!status) return
    if (isDownloading) return
    modal.confirm({
      title: t('detail.reinstallConfirm', { version }),
      content: t('detail.reinstallConfirmDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: () => doInstall(version),
    })
  }

  const handleSwitchVersion = (version: string) => {
    if (!status) return
    if (version === status.currentVersion) return
    modal.confirm({
      title: t('detail.switchConfirm', { sdk: status.displayName, version }),
      content: t('detail.switchConfirmDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      onOk: async () => {
        setSwitching(true)
        try {
          await SwitchVersion(status.sdkType, version)
          msgApi.success(t('detail.switchSuccess', { version }), 5)
          msgApi.info(t('detail.reopenTerminalHint'), 5)
          await onRefresh()
          fetchPackageManagers()
        } catch (e: any) {
          msgApi.error(t('detail.switchFail', { error: errMsg(e) }))
        } finally {
          setSwitching(false)
        }
      },
    })
  }

  const handleUninstallVersion = (version: string) => {
    if (!status) return
    const isActive = version === status.currentVersion
    modal.confirm({
      title: isActive
        ? t('detail.uninstallActiveConfirm', {
            sdk: status.displayName,
            version,
          })
        : t('detail.uninstallConfirm', { sdk: status.displayName, version }),
      content: isActive
        ? t('detail.uninstallActiveConfirmDesc')
        : t('detail.uninstallConfirmDesc'),
      okText: t('app.confirm'),
      okButtonProps: { danger: true },
      cancelText: t('app.cancel'),
      onOk: async () => {
        try {
          await UninstallVersion(status.sdkType, version)
          msgApi.success(t('detail.uninstallSuccess', { version }))
          onRefresh()
        } catch (e: any) {
          const msg = e?.message || String(e)
          if (msg.startsWith('ACTIVE_VERSION_DELETED:')) {
            msgApi.warning(
              t('detail.activeVersionDeleted', { sdk: status.displayName }),
            )
            onRefresh()
          } else {
            msgApi.error(t('detail.uninstallFail', { error: msg }))
          }
        }
      },
    })
  }

  const handleCopyDownloadUrl = async (version: string) => {
    if (!status) return
    let url = installProgress?.downloadUrl || ''
    if (!url || installProgress?.version !== version) {
      try {
        url = (await GetSdkDownloadURL(status.sdkType, version)) as string
      } catch {
        msgApi.error(t('detail.copyUrlFail'))
        return
      }
    }
    if (url) {
      try {
        await navigator.clipboard.writeText(url)
        msgApi.success(t('detail.copiedToClipboard'))
      } catch {
        msgApi.error(t('detail.copyUrlFail'))
      }
    }
  }

  // Import the SDK copy currently found in PATH into SVC's managed store.
  // Only offered when the SDK is pathConfigured && !configured (a PATH-only
  // copy exists but SVC isn't managing it yet). The confirm modal reuses
  // sidebar.* i18n keys since the wording is identical; cancelling leaves
  // the user on the detail page instead of stranding them like the old
  // sidebar-only flow did.
  const handleImportPath = () => {
    if (!status) return
    confirmAction({
      modal,
      title: t('sidebar.importConfirm', { sdk: status.displayName }),
      content: t('sidebar.importConfirmDesc'),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      run: async () => {
        try {
          await ImportPathSdk(status.sdkType)
          msgApi.success(
            t('sidebar.importSuccess', { sdk: status.displayName }),
          )
          onRefresh()
          fetchPackageManagers()
        } catch (e: any) {
          msgApi.error(t('sidebar.importFail', { error: errMsg(e) }))
          throw e
        }
      },
    })
  }

  const handleImportLocal = async (pick: 'file' | 'dir') => {
    if (!status) return
    setImporting(true)
    try {
      const selectedPath = (await (pick === 'file'
        ? SelectLocalFile()
        : SelectLocalDir())) as string
      if (!selectedPath) {
        setImporting(false)
        return
      }
      const ref = modal.confirm({
        title: t('detail.importingSdk', { sdk: status.displayName }),
        content: (
          <div style={{ textAlign: 'center', padding: '10px 0' }}>
            <Spin />
            <p style={{ marginTop: 8, color: '#888' }}>
              {t('detail.importingDesc')}
            </p>
          </div>
        ),
        okText: t('app.confirm'),
        cancelText: t('app.cancel'),
        cancelButtonProps: { disabled: true },
        okButtonProps: { loading: true },
        maskClosable: false,
        onOk: async () => {
          try {
            await ImportLocalSdk(status.sdkType, selectedPath)
            msgApi.success(t('detail.importSuccess'))
            onRefresh()
            fetchPackageManagers()
          } catch (e: any) {
            msgApi.error(t('detail.importFail', { error: errMsg(e) }))
            ref.update({
              cancelButtonProps: { disabled: false },
              okButtonProps: { loading: false },
            })
            throw e
          }
        },
      })
    } finally {
      setImporting(false)
    }
  }

  const handlePmInstall = async (name: string) => {
    setPmLoading(name)
    try {
      await InstallPackageManager(name)
      msgApi.success(t('detail.pmInstallSuccess', { name }))
      fetchPackageManagers()
    } catch (e: any) {
      msgApi.error(t('detail.pmInstallFail', { name, error: errMsg(e) }))
    } finally {
      setPmLoading('')
    }
  }

  const handlePmUpdate = async (name: string) => {
    setPmLoading(name)
    try {
      await UpdatePackageManager(name)
      msgApi.success(t('detail.pmUpdateSuccess', { name }))
      fetchPackageManagers()
    } catch (e: any) {
      msgApi.error(t('detail.pmUpdateFail', { name, error: errMsg(e) }))
    } finally {
      setPmLoading('')
    }
  }

  if (!status) {
    return (
      <div className="detail-panel">
        <div className="empty-state">
          <h3>{t('detail.selectSdk')}</h3>
          <p>{t('detail.selectSdkDesc')}</p>
        </div>
      </div>
    )
  }

  const showConflictModal = () => {
    modal.warning({
      title: t('detail.systemConflictTitle'),
      content: (
        <div>
          <p>{t('detail.systemConflictMsg')}</p>
          <ul
            style={{
              maxHeight: 200,
              overflow: 'auto',
              paddingLeft: 20,
              margin: '8px 0',
            }}
          >
            {conflicts.map((e: string, i: number) => (
              <li
                key={i}
                style={{ fontSize: 12, color: '#666', wordBreak: 'break-all' }}
              >
                {e}
              </li>
            ))}
          </ul>
        </div>
      ),
      okText: t('app.confirm'),
      width: 520,
    })
  }

  // Yellow "!" on the header: shows the exact file the PATH command resolves
  // to for a PATH-only SDK (import candidates and manager-owned copies).
  const showPathBinaryModal = () => {
    if (!status?.pathBinary) return
    modal.info({
      title: t('detail.pathBinaryTitle'),
      content: (
        <div>
          <p style={{ marginBottom: 8, color: '#888' }}>
            {t('detail.pathBinaryDesc')}
          </p>
          <Typography.Paragraph
            copyable
            style={{
              fontFamily: 'monospace',
              fontSize: 12,
              wordBreak: 'break-all',
            }}
          >
            {status.pathBinary}
          </Typography.Paragraph>
          {status.externalManager && (
            <p style={{ color: '#888' }}>
              {t('detail.externalManagerHint', {
                manager: externalManagerName(status.externalManager),
              })}
            </p>
          )}
        </div>
      ),
      okText: t('app.confirm'),
    })
  }

  const filteredVersions = versions.filter(
    (v) =>
      v.version.includes(searchText) || String(v.major).includes(searchText),
  )

  // "Latest" must be derived from the UNFILTERED list: using the filtered
  // index === 0 mislabels whatever row happens to match a search first.
  const latestVersion = versions.length > 0 ? versions[0].version : ''

  const installedSet = new Set(status.installedVersions || [])
  const currentVersion = status.currentVersion || ''

  return (
    <div className="detail-panel">
      {modalContextHolder}
      {/* Status Header */}
      <div className="status-header" style={{ position: 'relative' }}>
        {conflicts.length > 0 && (
          <Tooltip title={t('detail.systemConflictTitle')}>
            <WarningFilled
              onClick={showConflictModal}
              style={{
                position: 'absolute',
                top: 12,
                right: status.needsSwitch ? 36 : 12,
                fontSize: 18,
                color: '#ff4d4f',
                cursor: 'pointer',
              }}
            />
          </Tooltip>
        )}
        {status.needsSwitch && (
          <Tooltip title={t('detail.needsSwitch')}>
            <WarningOutlined
              style={{
                position: 'absolute',
                top: 12,
                right: 12,
                fontSize: 20,
                color: '#faad14',
                cursor: 'pointer',
              }}
            />
          </Tooltip>
        )}
        {status.pathConfigured && !status.configured && status.pathBinary && (
          <Tooltip title={t('detail.pathBinaryTooltip')}>
            <ExclamationCircleFilled
              onClick={showPathBinaryModal}
              style={{
                position: 'absolute',
                top: 12,
                right:
                  12 +
                  (status.needsSwitch ? 24 : 0) +
                  (conflicts.length > 0 ? 24 : 0),
                fontSize: 18,
                color: '#faad14',
                cursor: 'pointer',
              }}
            />
          </Tooltip>
        )}
        <h2>
          {status.displayName}
          <span
            className={`status-badge ${status.configured ? 'configured' : status.pathConfigured ? 'path-only' : 'not-configured'}`}
          >
            {status.configured ? (
              <>
                <CheckCircleFilled /> v{status.currentVersion}
              </>
            ) : status.pathConfigured ? (
              <>
                <CheckCircleFilled />{' '}
                {status.pathVersion ? `v${status.pathVersion}` : ''} (
                {status.externalManager
                  ? t('app.externalManager', {
                      manager: externalManagerName(status.externalManager),
                    })
                  : t('app.inPathOnly')}
                )
              </>
            ) : (
              <>
                <CloseCircleFilled /> {t('app.notConfigured')}
              </>
            )}
          </span>
        </h2>

        {status.installedVersions && status.installedVersions.length > 0 && (
          <div className="installed-versions">
            <span style={{ fontSize: 12, color: '#888', marginRight: 8 }}>
              {t('detail.installed')}:
            </span>
            {status.installedVersions.map((v) => {
              const isCurrent = v === currentVersion
              return (
                <Tooltip
                  key={v}
                  title={
                    isCurrent
                      ? t('detail.currentVersion')
                      : t('detail.clickToSwitch')
                  }
                >
                  <Tag
                    className={`installed-version-tag ${isCurrent ? 'current-version-tag' : ''}`}
                    style={{
                      cursor: isCurrent || switching ? 'default' : 'pointer',
                    }}
                    color={isCurrent ? 'green' : undefined}
                    onClick={() =>
                      !isCurrent && !switching && handleSwitchVersion(v)
                    }
                  >
                    {isCurrent && (
                      <CheckCircleFilled
                        style={{ marginRight: 4, fontSize: 10 }}
                      />
                    )}
                    {v}
                    <DeleteOutlined
                      style={{ marginLeft: 6, fontSize: 10, color: '#999' }}
                      onClick={(e) => {
                        e.stopPropagation()
                        handleUninstallVersion(v)
                      }}
                    />
                  </Tag>
                </Tooltip>
              )
            })}
          </div>
        )}
      </div>

      {/* Package Managers */}
      {packageManagers.length > 0 && (
        <div className="package-managers-section">
          <h3>{t('detail.packageManagers')}</h3>
          <div className="package-manager-list">
            {packageManagers.map((pm) => (
              <div key={pm.name} className="package-manager-item">
                <span className="pm-name">{pm.name}</span>
                {pm.installed ? (
                  <Tooltip title={t('detail.updateToLatest')}>
                    <Tag
                      color="green"
                      className="pm-tag-hover"
                      style={{ cursor: 'pointer' }}
                      onClick={() => handlePmUpdate(pm.name)}
                    >
                      v{pm.version}
                      <CloudUploadOutlined style={{ marginLeft: 4 }} />
                    </Tag>
                  </Tooltip>
                ) : (
                  <Tooltip title={t('detail.clickToInstall')}>
                    <Button
                      size="small"
                      type="primary"
                      icon={<DownloadOutlined />}
                      loading={pmLoading === pm.name}
                      onClick={() => handlePmInstall(pm.name)}
                      style={{ padding: '0 6px' }}
                    />
                  </Tooltip>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Version Section */}
      <div className="version-section">
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: 12,
          }}
        >
          <h3>{t('detail.availableVersions')}</h3>
          <div style={{ display: 'flex', gap: 8 }}>
            <Dropdown
              menu={{
                items: [
                  ...(status &&
                  !status.configured &&
                  status.pathConfigured &&
                  !status.externalManager &&
                  !status.systemProtected
                    ? [
                        {
                          key: 'path',
                          icon: <ImportOutlined />,
                          label: t('detail.importPath'),
                          onClick: handleImportPath,
                        },
                      ]
                    : []),
                  {
                    key: 'file',
                    icon: <FileOutlined />,
                    label: t('detail.importFile'),
                    onClick: () => handleImportLocal('file'),
                  },
                  {
                    key: 'dir',
                    icon: <FolderOpenOutlined />,
                    label: t('detail.importDir'),
                    onClick: () => handleImportLocal('dir'),
                  },
                ],
              }}
              trigger={['click']}
            >
              <Button
                icon={<ImportOutlined />}
                size="small"
                loading={importing}
              >
                {t('detail.importConfig')}
              </Button>
            </Dropdown>
            <Button
              icon={<ReloadOutlined />}
              size="small"
              onClick={() => fetchVersions()}
              loading={loading}
            >
              {t('detail.refresh')}
            </Button>
          </div>
        </div>

        <Input
          prefix={<SearchOutlined />}
          placeholder={t('detail.searchVersion')}
          value={searchText}
          onChange={(e) => setSearchText(e.target.value)}
          style={{ marginBottom: 12 }}
          allowClear
        />

        {loadError ? (
          <Result
            status="error"
            title={t('detail.loadError')}
            subTitle={
              loadErrorMessage ? (
                <div
                  style={{
                    maxWidth: 560,
                    margin: '0 auto',
                    color: '#888',
                    fontSize: 13,
                    wordBreak: 'break-word',
                    whiteSpace: 'pre-wrap',
                  }}
                >
                  {loadErrorMessage}
                </div>
              ) : undefined
            }
            style={{ padding: '40px 0' }}
            extra={
              <Button
                type="primary"
                icon={<ReloadOutlined />}
                onClick={() => fetchVersions()}
              >
                {t('detail.retry')}
              </Button>
            }
          />
        ) : loading ? (
          <div className="loading-container">
            <Spin />
          </div>
        ) : (
          <div className="version-list" style={{ paddingBottom: 20 }}>
            {filteredVersions.map((v) => {
              const isInstalled = installedSet.has(v.version)
              const isCurrentConfig = v.version === currentVersion
              const isLatest = v.version === latestVersion
              return (
                <div
                  key={v.version}
                  className={`version-row ${isLatest ? 'latest' : ''} ${isInstalled ? 'installed' : ''} ${isCurrentConfig ? 'current-config' : ''}`}
                >
                  <span className="version-number">
                    {v.version}
                    {isLatest && (
                      <Tag color="blue" style={{ marginLeft: 8 }}>
                        {t('detail.latest')}
                      </Tag>
                    )}
                    {isInstalled && (
                      <Tag color="green" style={{ marginLeft: 4 }}>
                        {t('detail.installed')}
                      </Tag>
                    )}
                    {isCurrentConfig && (
                      <Tag color="purple" style={{ marginLeft: 4 }}>
                        {t('detail.currentConfig')}
                      </Tag>
                    )}
                  </span>
                  {v.isLts && <span className="version-lts-badge">LTS</span>}
                  {v.releaseDate && (
                    <span className="version-date">{v.releaseDate}</span>
                  )}
                  {isInstalled ? (
                    <Tooltip title={t('detail.reinstallHover')}>
                      <Button
                        className="install-btn reinstall-btn"
                        size="small"
                        icon={<DownloadOutlined />}
                        loading={
                          installing && installProgress?.version === v.version
                        }
                        onClick={() => handleReinstall(v.version)}
                        disabled={installing || isDownloading}
                      >
                        <span className="reinstall-text">
                          {t('detail.installed')}
                        </span>
                        <span className="reinstall-hover-text">
                          {t('detail.reinstall')}
                        </span>
                      </Button>
                    </Tooltip>
                  ) : (
                    <Button
                      className="install-btn"
                      type={isLatest ? 'primary' : 'default'}
                      size="small"
                      icon={<DownloadOutlined />}
                      loading={
                        installing && installProgress?.version === v.version
                      }
                      onClick={() => handleInstall(v.version)}
                      disabled={installing || isDownloading}
                    >
                      {t('detail.install')}
                    </Button>
                  )}
                  <Tooltip title={t('detail.copyDownloadUrl')}>
                    <Button
                      className="copy-url-btn"
                      size="small"
                      type="text"
                      icon={<CopyOutlined />}
                      onClick={() => handleCopyDownloadUrl(v.version)}
                    />
                  </Tooltip>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {/* Progress Section */}
      {installProgress && (
        <div className="progress-section">
          <h4>
            {t('detail.installing', {
              sdk: status.displayName,
              version: installProgress.version,
            })}
          </h4>
          <Progress
            percent={installProgress.percent}
            status={
              installProgress.stage === 'error'
                ? 'exception'
                : installProgress.stage === 'done'
                  ? 'success'
                  : 'active'
            }
            strokeColor={{
              '0%': '#1677ff',
              '100%': '#52c41a',
            }}
          />
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              fontSize: 13,
              color: '#aaa',
              marginTop: 8,
            }}
          >
            <span>{translateProgress(installProgress)}</span>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              {installProgress.stage === 'downloading' &&
                installProgress.downloadedBytes > 0 && (
                  <span>
                    {formatBytes(installProgress.downloadedBytes)}
                    {installProgress.totalBytes > 0
                      ? ` / ${formatBytes(installProgress.totalBytes)}`
                      : ''}
                    {installProgress.speedBytesPerSec > 0
                      ? `  ${formatBytes(installProgress.speedBytesPerSec)}/s`
                      : ''}
                  </span>
                )}
              {installProgress.downloadUrl && (
                <Tooltip title={t('detail.copyDownloadUrl')}>
                  <Button
                    type="text"
                    size="small"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard
                        .writeText(installProgress.downloadUrl)
                        .then(() =>
                          msgApi.success(t('detail.copiedToClipboard')),
                        )
                        .catch(() => {})
                    }}
                    style={{ color: '#aaa', padding: '0 4px' }}
                  />
                </Tooltip>
              )}
              {installProgress.stage === 'downloading' && (
                <Tooltip title={t('detail.cancelInstall')}>
                  <Button
                    type="text"
                    size="small"
                    danger
                    icon={<CloseCircleFilled />}
                    // CancelInstall maps to a Go method with no return value;
                    // the promise never rejects, so there is nothing to catch.
                    onClick={() => CancelInstall(status.sdkType)}
                    style={{ padding: '0 4px' }}
                  />
                </Tooltip>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

export default DetailPanel
