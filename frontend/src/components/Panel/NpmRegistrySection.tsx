import { useCallback, useEffect, useState } from 'react'
import { App, Button, Input, Radio, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import { GetNpmRegistry, SetNpmRegistry } from '../../../wailsjs/go/main/App'
import { errMsg } from '../../utils/error'

const OFFICIAL = 'https://registry.npmjs.org'
const CHINA = 'https://registry.npmmirror.com'

// NpmRegistrySection shows and switches the npm registry of the active
// Node.js. The value is read live from `npm config get registry` on every
// mount — the component is keyed by currentVersion in DetailPanel — so it
// always reflects ground truth, including registries set outside SVC.
const NpmRegistrySection: React.FC = () => {
  const { t } = useTranslation()
  const { message: msgApi } = App.useApp()
  const [loading, setLoading] = useState(true)
  const [registry, setRegistry] = useState('')
  const [loadError, setLoadError] = useState('')
  const [customUrl, setCustomUrl] = useState('')
  const [applying, setApplying] = useState(false)

  const fetchRegistry = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const r = await GetNpmRegistry()
      setRegistry(r.replace(/\/+$/, ''))
    } catch (e) {
      setLoadError(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // Initial load on (re)mount; remounts are keyed by active version.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    fetchRegistry()
  }, [fetchRegistry])

  const mode =
    registry === OFFICIAL
      ? 'official'
      : registry === CHINA
        ? 'china'
        : registry
          ? 'custom'
          : ''

  useEffect(() => {
    // Seed the custom input once the live registry value has loaded.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (mode === 'custom') setCustomUrl(registry)
  }, [mode, registry])

  const apply = async (url: string) => {
    setApplying(true)
    try {
      await SetNpmRegistry(url)
      msgApi.success(t('detail.registrySwitchSuccess', { url }))
      await fetchRegistry()
    } catch (e) {
      msgApi.error(t('detail.registrySwitchFail', { error: errMsg(e) }))
    } finally {
      setApplying(false)
    }
  }

  const handleModeChange = (value: string) => {
    if (value === 'official') {
      apply(OFFICIAL)
    } else if (value === 'china') {
      apply(CHINA)
    }
    // 'custom' only reveals the URL input; applying is explicit there.
  }

  return (
    <div className="package-managers-section">
      <h3>{t('detail.npmRegistry')}</h3>
      {loading ? (
        <Spin size="small" />
      ) : loadError ? (
        <span style={{ color: '#ff4d4f' }}>{loadError}</span>
      ) : (
        <>
          <Radio.Group
            optionType="button"
            buttonStyle="solid"
            size="small"
            value={mode || undefined}
            disabled={applying}
            onChange={(e) => handleModeChange(e.target.value)}
          >
            <Radio.Button value="official">
              {t('detail.registryOfficial')}
            </Radio.Button>
            <Radio.Button value="china">
              {t('detail.registryChinaMirror')}
            </Radio.Button>
            <Radio.Button value="custom">
              {t('detail.registryCustom')}
            </Radio.Button>
          </Radio.Group>
          {mode === 'custom' && (
            <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
              <Input
                size="small"
                value={customUrl}
                placeholder={t('detail.registryCustomPlaceholder')}
                onChange={(e) => setCustomUrl(e.target.value)}
                onPressEnter={() => customUrl.trim() && apply(customUrl.trim())}
              />
              <Button
                size="small"
                type="primary"
                loading={applying}
                disabled={!customUrl.trim()}
                onClick={() => apply(customUrl.trim())}
              >
                {t('detail.registryApply')}
              </Button>
            </div>
          )}
          <div style={{ marginTop: 6, opacity: 0.6, fontSize: 12 }}>
            {t('detail.registryScopeHint')}
          </div>
        </>
      )}
    </div>
  )
}

export default NpmRegistrySection
