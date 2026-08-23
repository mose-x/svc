import { SdkStatus, SdkType } from '../../types/sdk'
import {
  SettingOutlined,
  HomeOutlined,
  ImportOutlined,
  SyncOutlined,
  WarningOutlined,
  InfoCircleOutlined,
} from '@ant-design/icons'
import { Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'
import {
  sdkColors,
  SDK_CATEGORIES,
  externalManagerName,
} from '../../constants/sdk'
import logoImg from '../../assets/logo.png'

interface SidebarProps {
  statuses: SdkStatus[]
  selectedSdk: SdkType | null
  downloadingSdks: Set<string>
  appVersion?: string
  onSelect: (sdkType: SdkType) => void
  onGoHome: () => void
  onOpenSettings: () => void
}

// SVG icons
const NodeIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <path
      d="M16 2L3 9.5v13L16 30l13-7.5v-13L16 2z"
      fill="currentColor"
      opacity="0.2"
    />
    <path
      d="M16 4.5L5 10.8v10.4L16 27.5l11-6.3V10.8L16 4.5z"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <text
      x="16"
      y="20"
      textAnchor="middle"
      fill="currentColor"
      fontSize="10"
      fontWeight="bold"
      fontFamily="monospace"
    >
      JS
    </text>
  </svg>
)

const JavaIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <path
      d="M12 24s-1.5 1 1 1.5c2 .4 4.5.5 7 0 0 0 .8-.5.5-.8-1.5-.8-6-1.2-8.5-0.7z"
      fill="currentColor"
      opacity="0.6"
    />
    <path
      d="M11 21s-2 1.2 1 1.5c3 .4 6 .3 8.5-.2 0 0 .6-.6.3-.8-2-1-7.5-1.3-9.8-0.5z"
      fill="currentColor"
      opacity="0.7"
    />
    <path
      d="M17 16c1.5 1.8-.5 3.5-.5 3.5s3.5-1.8 2-3.8c-1.5-2-3-2.8 3-6 0 0-7 1.8-4.5 6.3z"
      fill="currentColor"
    />
    <path
      d="M22 26.5s1 .8-1 1.2c-2.5.5-9 .8-13 0-1.2-.3 1-1.2 1.8-1.4.8-.2 1.2-.2 1.2-.2-1.5-1-9.5 2-4 2.8 8.5 1.5 15.5-.5 15-2.4z"
      fill="currentColor"
      opacity="0.5"
    />
    <path
      d="M18.5 13s1.5-2.5 2-3c1-1 1.5-2.5.5-3-1-.5-1.5 1-2 2.5s-1.5 3.5-0.5 3.5z"
      fill="currentColor"
    />
    <path
      d="M11.5 27.5c6 1 11-.5 11-1.5 0 0-.3.8-3.5 1.2-3.5.5-7.5.2-9-.2 0 0 .5.5 1.5.5z"
      fill="currentColor"
      opacity="0.4"
    />
  </svg>
)

const GoIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <ellipse cx="10" cy="14" rx="1.5" ry="2" fill="currentColor" />
    <ellipse cx="22" cy="14" rx="1.5" ry="2" fill="currentColor" />
    <path
      d="M8 19c0 0 2 4 8 4s8-4 8-4"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
    />
    <text
      x="16"
      y="28"
      textAnchor="middle"
      fill="currentColor"
      fontSize="7"
      fontWeight="bold"
      fontFamily="sans-serif"
    >
      Go
    </text>
  </svg>
)

const PythonIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <path
      d="M15.5 3C11 3 11 5.5 11 5.5V8h5v1H8S3 8.5 3 14s4 5.5 4 5.5h2.5v-3s-.2-3 3-3h5s2.8.2 2.8-2.8V6S21 3 15.5 3zm-2 2.5a1 1 0 110 2 1 1 0 010-2z"
      fill="currentColor"
      opacity="0.8"
    />
    <path
      d="M16.5 29c4.5 0 4.5-2.5 4.5-2.5V24h-5v-1h8s4.5-.5 4.5-6-4-5.5-4-5.5H22v3s.2 3-3 3h-5s-2.8-.2-2.8 2.8V26S12 29 16.5 29zm2-2.5a1 1 0 110-2 1 1 0 010 2z"
      fill="currentColor"
      opacity="0.6"
    />
  </svg>
)

const RustIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <circle cx="16" cy="16" r="12" fill="currentColor" opacity="0.2" />
    <path
      d="M16 5a11 11 0 1 0 0 22 11 11 0 0 0 0-22z"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <text
      x="16"
      y="21"
      textAnchor="middle"
      fill="currentColor"
      fontSize="12"
      fontWeight="bold"
      fontFamily="serif"
    >
      R
    </text>
  </svg>
)

const RubyIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <polygon points="16,4 28,16 16,28 4,16" fill="currentColor" opacity="0.3" />
    <polygon
      points="16,8 24,16 16,24 8,16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <text
      x="16"
      y="20"
      textAnchor="middle"
      fill="currentColor"
      fontSize="8"
      fontWeight="bold"
      fontFamily="serif"
    >
      Rb
    </text>
  </svg>
)

const DotNetIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <circle cx="16" cy="16" r="12" fill="currentColor" opacity="0.2" />
    <text
      x="16"
      y="21"
      textAnchor="middle"
      fill="currentColor"
      fontSize="9"
      fontWeight="bold"
      fontFamily="sans-serif"
    >
      .NET
    </text>
  </svg>
)

const PHPIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <ellipse cx="16" cy="16" rx="14" ry="8" fill="currentColor" opacity="0.2" />
    <ellipse
      cx="16"
      cy="16"
      rx="14"
      ry="8"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <text
      x="16"
      y="20"
      textAnchor="middle"
      fill="currentColor"
      fontSize="9"
      fontWeight="bold"
      fontFamily="sans-serif"
    >
      PHP
    </text>
  </svg>
)

const PerlIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <circle cx="16" cy="16" r="10" fill="currentColor" opacity="0.3" />
    <text
      x="16"
      y="21"
      textAnchor="middle"
      fill="currentColor"
      fontSize="9"
      fontWeight="bold"
      fontFamily="monospace"
    >
      Pl
    </text>
  </svg>
)

const MavenIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <path
      d="M16 3C12 3 9 6 9 10c0 3 1 5 2 7l5 12 5-12c1-2 2-4 2-7 0-4-3-7-7-7z"
      fill="currentColor"
      opacity="0.3"
    />
    <path
      d="M16 5c-3 0-5 2.5-5 5.5 0 2.5 1 4 2 5.5l3 8 3-8c1-1.5 2-3 2-5.5 0-3-2-5.5-5-5.5z"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <line
      x1="16"
      y1="10"
      x2="16"
      y2="18"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <path
      d="M13 8l3-3 3 3"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
)

const GradleIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <circle cx="12" cy="18" r="8" fill="currentColor" opacity="0.3" />
    <circle
      cx="12"
      cy="18"
      r="6"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <path d="M17 14h8v2h-6v4h4v2h-4v4h-2V14z" fill="currentColor" />
  </svg>
)

const FlutterIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <path
      d="M6 6h12L6 18l12 12H6L18 18 6 6z"
      fill="currentColor"
      opacity="0.6"
    />
    <path d="M14 6h12L14 18l12 12H14L26 18 14 6z" fill="currentColor" />
  </svg>
)

const AndroidIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <rect
      x="6"
      y="14"
      width="20"
      height="14"
      rx="3"
      fill="currentColor"
      opacity="0.3"
    />
    <rect
      x="6"
      y="14"
      width="20"
      height="14"
      rx="3"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <circle cx="12" cy="10" r="1.5" fill="currentColor" />
    <circle cx="20" cy="10" r="1.5" fill="currentColor" />
    <line
      x1="9"
      y1="7"
      x2="11"
      y2="9"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
    />
    <line
      x1="23"
      y1="7"
      x2="21"
      y2="9"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
    />
  </svg>
)

const DartIcon = () => (
  <svg viewBox="0 0 32 32" width="20" height="20">
    <rect
      x="6"
      y="6"
      width="20"
      height="20"
      rx="3"
      fill="currentColor"
      opacity="0.2"
    />
    <rect
      x="6"
      y="6"
      width="20"
      height="20"
      rx="3"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    />
    <text
      x="16"
      y="22"
      textAnchor="middle"
      fill="currentColor"
      fontSize="14"
      fontWeight="bold"
      fontFamily="sans-serif"
    >
      D
    </text>
  </svg>
)

const sdkIcons: Record<string, React.ReactNode> = {
  nodejs: <NodeIcon />,
  jdk: <JavaIcon />,
  go: <GoIcon />,
  python: <PythonIcon />,
  rust: <RustIcon />,
  ruby: <RubyIcon />,
  dotnet: <DotNetIcon />,
  php: <PHPIcon />,
  perl: <PerlIcon />,
  maven: <MavenIcon />,
  gradle: <GradleIcon />,
  flutter: <FlutterIcon />,
  android: <AndroidIcon />,
  dart: <DartIcon />,
}

const Sidebar: React.FC<SidebarProps> = ({
  statuses,
  selectedSdk,
  downloadingSdks,
  appVersion,
  onSelect,
  onGoHome,
  onOpenSettings,
}) => {
  const { t } = useTranslation()
  const configuredCount = statuses.filter(
    (s) => s.configured || s.pathConfigured,
  ).length

  return (
    <div className="sidebar">
      <div className="sidebar-header">
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <img
              src={appVersion ? `${logoImg}?v=${appVersion}` : logoImg}
              alt="logo"
              className="sidebar-logo"
            />
            <h1>{t('app.title')}</h1>
          </div>
          <div style={{ display: 'flex', gap: 4 }}>
            <Tooltip title={t('sidebar.home')}>
              <button className="sidebar-icon-btn" onClick={onGoHome}>
                <HomeOutlined />
              </button>
            </Tooltip>
            <Tooltip title={t('sidebar.settings')}>
              <button className="sidebar-icon-btn" onClick={onOpenSettings}>
                <SettingOutlined />
              </button>
            </Tooltip>
          </div>
        </div>
        <p>
          {t('sidebar.configuredCount', {
            count: configuredCount,
            total: statuses.length,
          })}
        </p>
      </div>
      <div className="sdk-list" style={{ paddingBottom: 20 }}>
        {SDK_CATEGORIES.map((cat) => (
          <div key={cat.key} className="sdk-category">
            <div className="category-header">{t(`categories.${cat.key}`)}</div>
            {cat.sdkTypes.map((sdkType) => {
              const status = statuses.find((s) => s.sdkType === sdkType)
              if (!status) return null
              return (
                <div
                  key={status.sdkType}
                  role="button"
                  tabIndex={0}
                  className={`sdk-item ${selectedSdk === status.sdkType ? 'active' : ''}`}
                  // All sidebar entries navigate to the detail page; the
                  // PATH-import action lives in the detail page's import
                  // dropdown so cancelling a confirm modal no longer strands
                  // the user on the home list.
                  onClick={() => onSelect(status.sdkType as SdkType)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      onSelect(status.sdkType as SdkType)
                    }
                  }}
                >
                  <div
                    className="sdk-item-icon"
                    style={{
                      background: sdkColors[status.sdkType] + '22',
                      color: sdkColors[status.sdkType],
                    }}
                  >
                    {sdkIcons[status.sdkType]}
                  </div>
                  <div className="sdk-item-info">
                    <div className="sdk-item-name">{status.displayName}</div>
                    {!status.configured && status.externalManager ? (
                      <Tooltip
                        title={t('sidebar.externalManagerTooltip', {
                          manager: externalManagerName(status.externalManager),
                        })}
                      >
                        <div className="sdk-item-version sdk-item-system">
                          <InfoCircleOutlined style={{ marginRight: 4 }} />
                          {status.pathVersion
                            ? `v${status.pathVersion} (${t('app.externalManager', { manager: externalManagerName(status.externalManager) })})`
                            : t('app.externalManager', {
                                manager: externalManagerName(
                                  status.externalManager,
                                ),
                              })}
                        </div>
                      </Tooltip>
                    ) : !status.configured && status.systemProtected ? (
                      <Tooltip title={t('sidebar.systemProtectedTooltip')}>
                        <div className="sdk-item-version sdk-item-system">
                          <WarningOutlined style={{ marginRight: 4 }} />
                          {status.pathVersion
                            ? `v${status.pathVersion} (${t('app.systemManaged')})`
                            : t('app.systemManaged')}
                        </div>
                      </Tooltip>
                    ) : !status.configured && status.pathConfigured ? (
                      <Tooltip title={t('sidebar.importTooltip')}>
                        <div className="sdk-item-version sdk-item-import">
                          <ImportOutlined style={{ marginRight: 4 }} />
                          {status.pathVersion
                            ? `v${status.pathVersion} (${t('app.inPathOnly')})`
                            : t('app.inPathOnly')}
                        </div>
                      </Tooltip>
                    ) : (
                      <div className="sdk-item-version">
                        {status.configured
                          ? `v${status.currentVersion}`
                          : t('app.notConfigured')}
                      </div>
                    )}
                  </div>
                  {downloadingSdks.has(status.sdkType) ? (
                    <SyncOutlined
                      spin
                      style={{ color: '#1677ff', fontSize: 14 }}
                    />
                  ) : status.needsSwitch ? (
                    <Tooltip title={t('sidebar.needsSwitch')}>
                      <WarningOutlined
                        style={{ color: '#faad14', fontSize: 16 }}
                      />
                    </Tooltip>
                  ) : (
                    <div
                      className={`sdk-item-status ${
                        status.configured
                          ? 'configured'
                          : status.pathConfigured
                            ? 'path-only'
                            : 'not-configured'
                      }`}
                    />
                  )}
                </div>
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}

export default Sidebar
