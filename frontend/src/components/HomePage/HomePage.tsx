import { useMemo } from 'react'
import { Card, Statistic, Row, Col, Tag, Tooltip } from 'antd'
import {
  CheckCircleFilled,
  CloseCircleFilled,
  ExclamationCircleFilled,
  CodeOutlined,
  ToolOutlined,
  MobileOutlined,
  InfoCircleOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { SdkStatus, SdkType } from '../../types/sdk'
import {
  sdkColors,
  SDK_CATEGORIES,
  externalManagerName,
} from '../../constants/sdk'
import logoImg from '../../assets/logo.png'

interface HomePageProps {
  statuses: SdkStatus[]
  appVersion?: string
  onSelect: (sdkType: SdkType) => void
  onOpenPathInfo: () => void
}

const CATEGORY_META: Record<string, { icon: React.ReactNode; color: string }> =
  {
    runtime: { icon: <CodeOutlined />, color: '#1677ff' },
    build: { icon: <ToolOutlined />, color: '#722ed1' },
    mobile: { icon: <MobileOutlined />, color: '#13c2c2' },
  }

const HomePage: React.FC<HomePageProps> = ({
  statuses,
  appVersion,
  onSelect,
  onOpenPathInfo,
}) => {
  const { t } = useTranslation()

  const stats = useMemo(() => {
    const configured = statuses.filter((s) => s.configured).length
    const pathOnly = statuses.filter(
      (s) => s.pathConfigured && !s.configured,
    ).length
    const notConfigured = statuses.filter(
      (s) => !s.configured && !s.pathConfigured,
    ).length
    const total = statuses.length
    return { configured, pathOnly, notConfigured, total }
  }, [statuses])

  return (
    <div className="home-page">
      <div className="home-hero">
        <img
          src={appVersion ? `${logoImg}?v=${appVersion}` : logoImg}
          alt="logo"
          className="home-logo"
        />
        <div style={{ flex: 1 }}>
          <h1 className="home-title">{t('home.welcome')}</h1>
          <p className="home-subtitle">{t('home.subtitle')}</p>
        </div>
        <Tooltip title={t('sidebar.pathInfo')}>
          <button className="home-path-btn" onClick={onOpenPathInfo}>
            <InfoCircleOutlined />
          </button>
        </Tooltip>
      </div>

      <Row gutter={[16, 16]} className="home-stats">
        <Col xs={24} sm={12} lg={6}>
          <Card className="home-stat-card" variant="borderless">
            <Statistic
              title={t('home.totalSdks')}
              value={stats.total}
              suffix={<span style={{ fontSize: 14, color: '#888' }}></span>}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card
            className="home-stat-card home-stat-card-success"
            variant="borderless"
          >
            <Statistic
              title={t('home.configured')}
              value={stats.configured}
              valueStyle={{ color: '#52c41a' }}
              prefix={<CheckCircleFilled />}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card
            className="home-stat-card home-stat-card-warning"
            variant="borderless"
          >
            <Statistic
              title={t('home.pathOnly')}
              value={stats.pathOnly}
              valueStyle={{ color: '#faad14' }}
              prefix={<ExclamationCircleFilled />}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card
            className="home-stat-card home-stat-card-error"
            variant="borderless"
          >
            <Statistic
              title={t('home.notConfigured')}
              value={stats.notConfigured}
              valueStyle={{ color: '#ff4d4f' }}
              prefix={<CloseCircleFilled />}
            />
          </Card>
        </Col>
      </Row>

      <div className="home-categories">
        {SDK_CATEGORIES.map((cat) => {
          const meta = CATEGORY_META[cat.key]
          const catStatuses = statuses.filter((s) =>
            cat.sdkTypes.includes(s.sdkType),
          )
          const catConfigured = catStatuses.filter(
            (s) => s.configured || s.pathConfigured,
          ).length

          return (
            <Card
              key={cat.key}
              className="home-category-card"
              title={
                <div className="home-category-title">
                  <span style={{ color: meta.color }}>{meta.icon}</span>
                  <span>{t(`categories.${cat.key}`)}</span>
                  <Tag style={{ marginLeft: 'auto' }}>
                    {catConfigured}/{catStatuses.length}
                  </Tag>
                </div>
              }
            >
              <div className="home-sdk-grid">
                {catStatuses.map((s) => (
                  <Tooltip
                    key={s.sdkType}
                    title={
                      s.configured
                        ? `${s.displayName} v${s.currentVersion}`
                        : s.pathConfigured
                          ? s.externalManager
                            ? `${s.displayName} (${t('app.externalManager', { manager: externalManagerName(s.externalManager) })})`
                            : `${s.displayName} (${t('app.inPathOnly')})`
                          : `${s.displayName} - ${t('app.notConfigured')}`
                    }
                  >
                    <div
                      role="button"
                      tabIndex={0}
                      className={`home-sdk-chip ${s.configured ? 'configured' : s.pathConfigured ? 'path-only' : ''}`}
                      onClick={() => onSelect(s.sdkType as SdkType)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          onSelect(s.sdkType as SdkType)
                        }
                      }}
                    >
                      <div
                        className="home-sdk-dot"
                        style={{ background: sdkColors[s.sdkType] || '#888' }}
                      />
                      <span className="home-sdk-name">{s.displayName}</span>
                      {s.configured && (
                        <span className="home-sdk-ver">
                          v{s.currentVersion}
                        </span>
                      )}
                    </div>
                  </Tooltip>
                ))}
              </div>
            </Card>
          )
        })}
      </div>
    </div>
  )
}

export default HomePage
