import { useCallback, useEffect, useState } from 'react'
import { App, Button, Empty, Input, Modal, Spin, Tag, Tooltip } from 'antd'
import {
  CloudUploadOutlined,
  DeleteOutlined,
  DownloadOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  GetGlobalPackages,
  InstallGlobalPackage,
  UninstallGlobalPackage,
  UpdateGlobalPackage,
} from '../../../wailsjs/go/main/App'
import { GlobalPackage } from '../../types/sdk'
import { confirmAction } from '../../utils/confirmAction'
import { errMsg } from '../../utils/error'

// GlobalPackagesSection lists the globally installed npm packages of the
// active Node.js and supports install-by-name, update-to-latest and
// uninstall. Remounted (re-fetched) on version switch via its key in
// DetailPanel.
const GlobalPackagesSection: React.FC = () => {
  const { t } = useTranslation()
  const { message: msgApi } = App.useApp()
  const [modal, modalContextHolder] = Modal.useModal()
  const [packages, setPackages] = useState<GlobalPackage[]>([])
  const [loading, setLoading] = useState(true)
  const [rowLoading, setRowLoading] = useState('')
  const [installName, setInstallName] = useState('')
  const [installing, setInstalling] = useState(false)

  const fetchPackages = useCallback(async () => {
    setLoading(true)
    try {
      const list = await GetGlobalPackages('nodejs')
      setPackages(((list as unknown as GlobalPackage[]) ?? []).slice())
    } catch {
      setPackages([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // Initial load on (re)mount; remounts are keyed by active version.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    fetchPackages()
  }, [fetchPackages])

  const handleInstall = async () => {
    const name = installName.trim()
    if (!name || installing) return
    setInstalling(true)
    try {
      await InstallGlobalPackage(name)
      msgApi.success(t('detail.gpInstallSuccess', { name }))
      setInstallName('')
      await fetchPackages()
    } catch (e) {
      msgApi.error(t('detail.gpInstallFail', { name, error: errMsg(e) }))
    } finally {
      setInstalling(false)
    }
  }

  const handleUpdate = async (name: string) => {
    if (rowLoading) return
    setRowLoading(name)
    try {
      await UpdateGlobalPackage(name)
      msgApi.success(t('detail.pmUpdateSuccess', { name }))
      await fetchPackages()
    } catch (e) {
      msgApi.error(t('detail.pmUpdateFail', { name, error: errMsg(e) }))
    } finally {
      setRowLoading('')
    }
  }

  const handleUninstall = (name: string) => {
    confirmAction({
      modal,
      title: t('detail.gpUninstallConfirm', { name }),
      content: t('detail.gpUninstallConfirmDesc', { name }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      run: async () => {
        try {
          await UninstallGlobalPackage(name)
          msgApi.success(t('detail.gpUninstallSuccess', { name }))
          await fetchPackages()
        } catch (e) {
          msgApi.error(t('detail.gpUninstallFail', { name, error: errMsg(e) }))
          throw e
        }
      },
    })
  }

  return (
    <div className="package-managers-section">
      <h3>{t('detail.globalPackages')}</h3>
      {modalContextHolder}
      <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
        <Input
          size="small"
          value={installName}
          placeholder={t('detail.gpInstallPlaceholder')}
          onChange={(e) => setInstallName(e.target.value)}
          onPressEnter={handleInstall}
        />
        <Button
          size="small"
          type="primary"
          icon={<DownloadOutlined />}
          loading={installing}
          disabled={!installName.trim()}
          onClick={handleInstall}
        />
      </div>
      {loading ? (
        <Spin size="small" />
      ) : packages.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={t('detail.globalPackagesEmpty')}
        />
      ) : (
        <div className="package-manager-list">
          {packages.map((pkg) => (
            <div key={pkg.name} className="package-manager-item">
              <span className="pm-name">{pkg.name}</span>
              <Tooltip title={t('detail.updateToLatest')}>
                <Tag
                  color="green"
                  className="pm-tag-hover"
                  style={{ cursor: 'pointer' }}
                  onClick={() => handleUpdate(pkg.name)}
                >
                  v{pkg.version}
                  <CloudUploadOutlined style={{ marginLeft: 4 }} />
                </Tag>
              </Tooltip>
              <Tooltip title={t('detail.gpUninstall')}>
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  style={{ padding: '0 4px' }}
                  onClick={() => handleUninstall(pkg.name)}
                />
              </Tooltip>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default GlobalPackagesSection
