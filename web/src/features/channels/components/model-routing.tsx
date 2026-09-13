/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

import { NumericSpinnerInput } from './numeric-spinner-input'

type RouteOption = { group: string; model: string }
type RouteRow = {
  channel_id: number
  name: string
  tag: string | null
  status: number
  priority: number | null
  weight: number | null
}
type WriteResult = {
  success: boolean
  committed?: boolean
  degraded?: boolean
  refresh_error?: string
  message?: string
}
const config = {
  skipBusinessError: true,
  skipErrorHandler: true,
  disableDuplicate: true,
}
async function readRoutes<T>(url: string, params?: RouteOption): Promise<T[]> {
  const res = await api.get<{ success: boolean; data: T[]; message?: string }>(
    url,
    { ...config, params }
  )
  if (!res.data.success) {
    throw new Error(res.data.message || 'Unable to read routes')
  }
  return res.data.data
}

export function ModelRouting(props: { canWrite: boolean }) {
  const { t } = useTranslation()
  const [group, setGroup] = useState('')
  const [model, setModel] = useState('')
  const options = useQuery({
    queryKey: ['channel-route-options'],
    queryFn: () =>
      readRoutes<RouteOption>('/api/channel/model_priority/options'),
  })
  const groups = [...new Set(options.data?.map((item) => item.group))]
  const models =
    options.data
      ?.filter((item) => item.group === group)
      .map((item) => item.model) ?? []
  return (
    <section
      className='flex min-h-0 flex-col gap-4 overflow-auto'
      aria-label={t('Model routing')}
    >
      <p className='text-muted-foreground text-sm'>
        {t(
          'Higher priority routes are tried first. Weight distributes traffic within the same priority. Disabled channels remain visible but do not receive traffic.'
        )}
      </p>
      <div className='flex flex-wrap gap-4'>
        <label className='flex flex-col gap-1'>
          {t('Group')}
          <select
            className='border-input bg-background rounded-md border p-2'
            value={group}
            onChange={(event) => {
              setGroup(event.target.value)
              setModel('')
            }}
          >
            <option value=''>{t('Select group')}</option>
            {groups.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
        <label className='flex flex-col gap-1'>
          {t('Physical model')}
          <select
            className='border-input bg-background rounded-md border p-2'
            value={model}
            disabled={!group}
            onChange={(event) => setModel(event.target.value)}
          >
            <option value=''>{t('Select model')}</option>
            {models.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
      </div>
      {options.isPending && <p role='status'>{t('Loading...')}</p>}
      {options.isError && (
        <div role='alert'>
          {t('Unable to read route options')}{' '}
          <Button onClick={() => void options.refetch()}>{t('Refresh')}</Button>
        </div>
      )}
      {options.isSuccess && !options.data.length && (
        <p>{t('No configured model routes')}</p>
      )}
      {group && model && (
        <RouteTable
          key={JSON.stringify([group, model])}
          group={group}
          model={model}
          canWrite={props.canWrite}
        />
      )}
    </section>
  )
}

function RouteTable(props: RouteOption & { canWrite: boolean }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [notice, setNotice] = useState('')
  const queryKey = ['channel-model-priority', props.group, props.model]
  const query = useQuery({
    queryKey,
    queryFn: () =>
      readRoutes<RouteRow>('/api/channel/model_priority', {
        group: props.group,
        model: props.model,
      }),
    staleTime: 0,
    retry: false,
  })
  const mutation = useMutation({
    mutationFn: async (data: { channel_id: number; priority: number }) => {
      setNotice('')
      const response = await api.put<WriteResult>(
        '/api/channel/model_priority',
        {
          group: props.group,
          model: props.model,
          channel_id: data.channel_id,
          priority: data.priority,
        },
        config
      )
      if (!response.data.success && !response.data.committed) {
        throw new Error(response.data.message || t('Failed to save route'))
      }
      const degraded = response.data.degraded
      setNotice(
        degraded
          ? t(
              'Saved, but cache refresh failed. Do not resend the write; refresh to verify.'
            )
          : t('Saved. Reading back...')
      )
      await client.invalidateQueries({ queryKey: ['channels'] })
      const readback = await query.refetch()
      if (!readback.isError && !degraded) setNotice(t('Saved and read back'))
    },
    onError: (error: Error) => setNotice(error.message),
    retry: false,
  })
  return (
    <>
      {notice && <p role='status'>{notice}</p>}
      <Button
        variant='outline'
        disabled={query.isFetching || mutation.isPending}
        onClick={() => void query.refetch()}
      >
        {t('Refresh')}
      </Button>
      {query.isFetching && <p role='status'>{t('Loading...')}</p>}
      {query.isError && (
        <p role='alert'>
          {t(
            'Unable to read routes. Refresh to verify saved values; do not resend a committed write.'
          )}
        </p>
      )}
      {!query.isError && !query.isFetching && query.data && (
        <>
          {!query.data.length && (
            <p>{t('No candidate channels for this group and model')}</p>
          )}
          {!!query.data.length &&
            !query.data.some((row) => row.status === 1) && (
              <p>{t('No enabled candidate channels')}</p>
            )}
          {!!query.data.length && (
            <div className='overflow-x-auto'>
              <table className='w-full text-left text-sm'>
                <thead>
                  <tr>
                    {[
                      'Channel',
                      'Tag',
                      'Status',
                      'Priority',
                      'Weight',
                      'Actions',
                    ].map((label) => (
                      <th className='p-2' key={label}>
                        {t(label)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {query.data.map((row) => (
                    <RoutingRow
                      key={`${row.channel_id}:${row.priority}`}
                      row={row}
                      canWrite={props.canWrite}
                      disabled={!props.canWrite || mutation.isPending}
                      save={(priority) =>
                        mutation.mutate({
                          channel_id: row.channel_id,
                          priority,
                        })
                      }
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  )
}

function RoutingRow(props: {
  row: RouteRow
  canWrite: boolean
  disabled: boolean
  save: (priority: number) => void
}) {
  const { t } = useTranslation()
  const [priority, setPriority] = useState(props.row.priority ?? 0)
  return (
    <tr className='border-b'>
      <td className='p-2'>
        {props.row.name}{' '}
        <span className='text-muted-foreground'>#{props.row.channel_id}</span>
      </td>
      <td className='p-2'>{props.row.tag || '—'}</td>
      <td className='p-2'>
        {props.row.status === 1 ? t('Enabled') : t('Disabled')}
      </td>
      <td className='p-2'>
        <NumericSpinnerInput
          ariaLabel={t('Priority for {{channel}}', { channel: props.row.name })}
          value={priority}
          min={Number.MIN_SAFE_INTEGER}
          max={Number.MAX_SAFE_INTEGER}
          onChange={setPriority}
          disabled={props.disabled}
        />
      </td>
      <td className='p-2'>{props.row.weight ?? 0}</td>
      <td className='p-2'>
        {props.canWrite && (
          <Button
            disabled={props.disabled || priority === (props.row.priority ?? 0)}
            onClick={() => props.save(priority)}
          >
            {t('Save')}
          </Button>
        )}
      </td>
    </tr>
  )
}
