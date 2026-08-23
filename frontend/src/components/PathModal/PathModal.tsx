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
      // Only show SDK paths not yet imported into SVC
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
      fetchEntries()
    }
  }, [open, fetchEntries])

  const handleImport = (entry: PathEntry) => {
    if (!entry.sdkType) return
    const sdkName = sdkDisplayNames[entry.sdkType] || entry.sdkType
    const ref = modal.confirm({
      title: t('path.importConfirm', { sdk: sdkName }),
      content: t('path.importConfirmDesc', { path: entry.path }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      maskClosable: false,
      onOk: async () => {
        ref.update({
          cancelButtonProps: { disabled: true },
          okButtonProps: { loading: true },
        })
        setImporting(entry.path)
        try {
          await ImportSdk(entry.path, entry.sdkType)
          msgApi.success(t('path.importSuccess'))
          fetchEntries()
          onRefresh()
        } catch (e: any) {
          msgApi.error(t('path.importFail', { error: e?.message || e }))
          ref.update({
            cancelButtonProps: { disabled: false },
            okButtonProps: { loading: false },
          })
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
      width: 100,
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
      dataIndex: 'isManaged',
      key: 'status',
      width: 120,
      align: 'right' as const,
      // isManaged is always false here — managed PATH entries are filtered
      // out in fetchEntries (see `!e.isManaged`) — so only the import-button
      // / externally-managed / system-tag branches below can ever render.
      render: (_managed: boolean, entry: PathEntry) =>
        entry.externalManager ? (
          <Tag>
            {t('path.externallyManaged', {
              manager: externalManagerName(entry.externalManager),
            })}
          </Tag>
        ) : entry.systemProtected ? (
          <Tag>{t('app.systemManaged')}</Tag>
        ) : entry.sdkType ? (
          <Button
            size="small"
            type="primary"
            icon={<ImportOutlined />}
            loading={importing === entry.path}
            onClick={() => handleImport(entry)}
          >
            {importing === entry.path ? t('path.importing') : t('path.import')}
          </Button>
        ) : (
          <Tag>{t('path.system')}</Tag>
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
            rowKey={(r: any) => `${r.path}|${r.sdkType}`}
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
