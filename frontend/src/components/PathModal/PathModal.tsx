import { useState, useEffect, useCallback, useMemo } from 'react'
import { Modal, Table, Tag, Button, App, Empty, Dropdown } from 'antd'
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

// PathRow is one table row: all SDK entries sharing the same directory are
// merged into a single row (union of sdkTypes) so /usr/bin etc. appear once.
interface PathRow {
  path: string
  sdkTypes: string[]
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

  // Merge entries that share the same directory into one row: /usr/bin
  // provides python3 + ruby + perl + ... and must not produce N duplicate
  // rows. Flags are combined (OR for systemProtected, first manager wins).
  const rows = useMemo<PathRow[]>(() => {
    const byPath = new Map<string, PathRow>()
    for (const e of entries) {
      let row = byPath.get(e.path)
      if (!row) {
        row = {
          path: e.path,
          sdkTypes: [],
          systemProtected: false,
          externalManager: '',
        }
        byPath.set(e.path, row)
      }
      if (!row.sdkTypes.includes(e.sdkType)) {
        row.sdkTypes.push(e.sdkType)
      }
      row.systemProtected = row.systemProtected || e.systemProtected
      if (!row.externalManager && e.externalManager) {
        row.externalManager = e.externalManager
      }
    }
    return Array.from(byPath.values())
  }, [entries])

  useEffect(() => {
    if (open) {
      fetchEntries()
    }
  }, [open, fetchEntries])

  const handleImport = (path: string, sdkType: string) => {
    if (!sdkType) return
    const sdkName = sdkDisplayNames[sdkType] || sdkType
    confirmAction({
      modal,
      title: t('path.importConfirm', { sdk: sdkName }),
      content: t('path.importConfirmDesc', { path }),
      okText: t('app.confirm'),
      cancelText: t('app.cancel'),
      run: async () => {
        setImporting(path)
        try {
          await ImportSdk(path, sdkType)
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

  const renderAction = (row: PathRow) => {
    if (row.externalManager) {
      return (
        <Tag>
          {t('path.externallyManaged', {
            manager: externalManagerName(row.externalManager),
          })}
        </Tag>
      )
    }
    if (row.systemProtected) {
      return <Tag>{t('app.systemManaged')}</Tag>
    }
    if (row.sdkTypes.length === 1) {
      return (
        <Button
          size="small"
          type="primary"
          icon={<ImportOutlined />}
          loading={importing === row.path}
          onClick={() => handleImport(row.path, row.sdkTypes[0])}
        >
          {importing === row.path ? t('path.importing') : t('path.import')}
        </Button>
      )
    }
    // One directory provides several SDKs: let the user pick which to import.
    return (
      <Dropdown
        trigger={['click']}
        menu={{
          items: row.sdkTypes.map((type) => ({
            key: type,
            icon: <ImportOutlined />,
            label: `${t('path.import')} ${sdkDisplayNames[type] || type}`,
          })),
          onClick: ({ key }) => handleImport(row.path, key),
        }}
      >
        <Button size="small" type="primary" loading={importing === row.path}>
          {importing === row.path
            ? t('path.importing')
            : t('path.importChoose')}
        </Button>
      </Dropdown>
    )
  }

  const columns = [
    {
      title: t('path.sdkColumn'),
      dataIndex: 'sdkTypes',
      key: 'sdkTypes',
      width: 160,
      render: (types: string[]) => (
        <>
          {types.map((type) => (
            <Tag key={type} color={sdkColors[type] || '#666'}>
              {sdkDisplayNames[type] || type}
            </Tag>
          ))}
        </>
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
      render: (_: unknown, row: PathRow) => renderAction(row),
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
        {rows.length === 0 && !loading ? (
          <Empty description={t('path.emptyPath')} />
        ) : (
          <Table
            dataSource={rows}
            columns={columns}
            rowKey={(r: PathRow) => r.path}
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
