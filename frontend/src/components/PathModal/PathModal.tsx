import { useState, useEffect, useCallback } from 'react'
import { Modal, Table, Tag, Button, App, Empty } from 'antd'
import { ImportOutlined, FolderOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { GetPathEntries, ImportSdk } from '../../../wailsjs/go/main/App'
import {
  sdkColors,
  sdkDisplayNames,
  externalManagerName,
} from '../../constants/sdk'
import { confirmAction } from '../../utils/confirmAction'
import { errMsg } from '../../utils/error'

interface PathEntry {
  path: string
  isManaged: boolean
  sdkType: string
  systemProtected: boolean
  externalManager: string
}

interface PathModalProps {
  open: boolean
  onClose: () => void
  onRefresh: () => void
}

const PathModal: React.FC<PathModalProps> = ({ open, onClose, onRefresh }) => {
  const [entries, setEntries] = useState<PathEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [importing, setImporting] = useState<string | null>(null)
  const { message: msgApi } = App.useApp()
  const [modal, modalContextHolder] = Modal.useModal()
  const { t } = useTranslation()

  const fetchEntries = useCallback(async () => {
    setLoading(true)
    try {
      const result = await GetPathEntries()
      // Only show SDK paths not yet imported into SVC. OS-protected dirs
      // (/usr/bin, ...) are already excluded by the backend.
      const filtered = (result || []).filter((e) => e.sdkType && !e.isManaged)
      setEntries(filtered)
    } catch (e) {
      console.error('Failed to get PATH information:', e)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (open) {
      // Load-on-open: setLoading(true) fires synchronously but the fetch is
      // the whole point of this effect.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      fetchEntries()
    }
  }, [open, fetchEntries])

  const handleImport = (entry: PathEntry) => {
    if (!entry.sdkType) return
    const sdkName = sdkDisplayNames[entry.sdkType] || entry.sdkType
    confirmAction({
      modal,
      title: t('path.importConfirm', { sdk: sdkName }),
      content: t('path.importConfirmDesc', { path: entry.path }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      run: async () => {
        setImporting(entry.path + '|' + entry.sdkType)
        try {
          await ImportSdk(entry.path, entry.sdkType)
          msgApi.success(t('path.importSuccess'))
          fetchEntries()
          onRefresh()
        } catch (e: any) {
          msgApi.error(t('path.importFail', { error: errMsg(e) }))
          throw e
        } finally {
          setImporting(null)
        }
      },
    })
  }

  const columns = [
    {
      title: t('path.sdkColumn'),
      dataIndex: 'sdkType',
      key: 'sdkType',
      width: 120,
      render: (type: string) =>
        type ? (
          <Tag color={sdkColors[type] || '#666'}>
            {sdkDisplayNames[type] || type}
          </Tag>
        ) : (
          <Tag>{t('path.noSdkDetected')}</Tag>
        ),
    },
    {
      title: t('path.title'),
      dataIndex: 'path',
      key: 'path',
      ellipsis: true,
      render: (path: string) => (
        <span style={{ fontFamily: 'monospace', fontSize: 12 }}>
          <FolderOutlined style={{ marginRight: 6 }} />
          {path}
        </span>
      ),
    },
    {
      title: '',
      key: 'status',
      width: 140,
      align: 'right' as const,
      render: (_: unknown, entry: PathEntry) =>
        entry.externalManager ? (
          <Tag>
            {t('path.externallyManaged', {
              manager: externalManagerName(entry.externalManager),
            })}
          </Tag>
        ) : entry.systemProtected ? (
          // Safety branch: the backend skips protected dirs, but never offer
          // an import button if one ever slips through.
          <Tag>{t('app.systemManaged')}</Tag>
        ) : (
          <Button
            size="small"
            type="primary"
            icon={<ImportOutlined />}
            loading={importing === entry.path + '|' + entry.sdkType}
            onClick={() => handleImport(entry)}
          >
            {importing === entry.path + '|' + entry.sdkType
              ? t('path.importing')
              : t('path.import')}
          </Button>
        ),
    },
  ]

  return (
    <>
      {modalContextHolder}
      <Modal
        title={t('path.title')}
        open={open}
        onCancel={onClose}
        footer={null}
        width={800}
      >
        {entries.length === 0 && !loading ? (
          <Empty description={t('path.emptyPath')} />
        ) : (
          <Table
            dataSource={entries}
            columns={columns}
            rowKey={(r: PathEntry) => `${r.path}|${r.sdkType}`}
            loading={loading}
            pagination={false}
            size="small"
            scroll={{ y: 400 }}
          />
        )}
      </Modal>
    </>
  )
}

export default PathModal
