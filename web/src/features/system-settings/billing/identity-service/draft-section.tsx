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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { newIdentity, newRow, type DraftRow, type Section } from './draft'

const titles: Record<Section, string> = {
  service_defaults: 'Service defaults (S)',
  identity_defaults: 'Identity defaults (D)',
  service_models: 'Service model availability and S overrides',
  identity_model_ratios: 'Identity model D overrides',
  model_identity_scopes: 'Model identity eligibility',
}
export function DraftSection(props: {
  section: Section
  rows: DraftRow[]
  groups: string[]
  change: (rows: DraftRow[]) => void
}) {
  const { t } = useTranslation()
  const [group, setGroup] = useState('')
  const grouped =
    props.section === 'service_models' ||
    props.section === 'identity_model_ratios'
  const scopes = props.section === 'model_identity_scopes'
  const optional = grouped
  const update = (id: number, patch: Partial<DraftRow>) =>
    props.change(props.rows.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  const rows = props.rows.filter((r) => !grouped || r.group === group)
  return (
    <section
      aria-label={t(titles[props.section])}
      className='space-y-3 rounded-lg border p-4'
    >
      <h3 className='font-medium'>{t(titles[props.section])}</h3>
      {grouped && (
        <label className='block space-y-1 text-sm'>
          <span>{t('Selected exact group')}</span>
          <Input
            aria-label={t('Selected exact group')}
            value={group}
            list={`${props.section}-groups`}
            onChange={(e) => setGroup(e.target.value)}
          />
          <datalist id={`${props.section}-groups`}>
            {[
              ...new Set([...props.groups, ...props.rows.map((r) => r.group)]),
            ].map((g) => (
              <option key={g} value={g} />
            ))}
          </datalist>
          <span className='text-muted-foreground'>
            {t('Type any exact key. Switching groups keeps all draft rows.')}
          </span>
        </label>
      )}
      {rows.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No configured rows in this selection.')}
        </p>
      )}
      {rows.map((row) => (
        <div
          key={row.id}
          role='group'
          aria-label={t('Configuration row')}
          className='grid min-w-0 gap-3 rounded-lg border p-3 sm:grid-cols-2 lg:grid-cols-3'
        >
          <label className='space-y-1 text-sm'>
            <span>{t('Exact key')}</span>
            <Input
              value={row.key}
              onChange={(e) => update(row.id, { key: e.target.value })}
            />
          </label>
          {!scopes && (
            <label className='space-y-1 text-sm'>
              <span>{t('Ratio')}</span>
              <Input
                aria-label={t('Ratio')}
                inputMode='decimal'
                value={row.ratio}
                placeholder={
                  optional ? t('Inherit default') : t('Required default')
                }
                onChange={(e) => update(row.id, { ratio: e.target.value })}
              />
              <span className='text-muted-foreground'>
                {row.ratio === '' && optional
                  ? t('Inherit default')
                  : t('Explicit value: 0 is free; 1 is no discount')}
              </span>
            </label>
          )}
          {props.section === 'service_models' && (
            <label className='flex items-center gap-2 text-sm'>
              <input
                type='checkbox'
                checked={row.enabled}
                onChange={(e) => update(row.id, { enabled: e.target.checked })}
              />
              {t('Explicitly open model')}
            </label>
          )}
          {scopes && (
            <>
              <label className='space-y-1 text-sm'>
                <span>{t('Eligibility mode')}</span>
                <select
                  className='border-input bg-background block h-8 w-full rounded-lg border px-2 focus-visible:outline'
                  value={row.mode}
                  onChange={(e) =>
                    update(row.id, { mode: e.target.value as DraftRow['mode'] })
                  }
                >
                  <option value='public'>
                    {t('Public: all registered identities')}
                  </option>
                  <option value='restricted'>
                    {t('Restricted: listed identities only')}
                  </option>
                </select>
              </label>
              <div className='space-y-2 sm:col-span-2 lg:col-span-3'>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Restricted with no identities denies everyone. Public requires an empty list.'
                  )}
                </p>
                {row.identities.map((identity, index) => (
                  <div className='flex gap-2' key={identity.id}>
                    <label className='min-w-0 flex-1 text-sm'>
                      <span>{t('Exact identity')}</span>
                      <Input
                        value={identity.value}
                        onChange={(e) =>
                          update(row.id, {
                            identities: row.identities.map((v, i) =>
                              i === index ? { ...v, value: e.target.value } : v
                            ),
                          })
                        }
                      />
                    </label>
                    <Button
                      type='button'
                      variant='outline'
                      onClick={() =>
                        update(row.id, {
                          identities: row.identities.filter(
                            (_, i) => i !== index
                          ),
                        })
                      }
                    >
                      {t('Remove identity')}
                    </Button>
                  </div>
                ))}
                <Button
                  type='button'
                  variant='outline'
                  onClick={() =>
                    update(row.id, {
                      identities: [...row.identities, newIdentity()],
                    })
                  }
                >
                  {t('Add identity')}
                </Button>
              </div>
            </>
          )}
          <div className='flex flex-wrap gap-2 sm:col-span-2 lg:col-span-3'>
            {optional && (
              <Button
                type='button'
                variant='outline'
                onClick={() => update(row.id, { ratio: '' })}
              >
                {t('Remove override / inherit')}
              </Button>
            )}
            <Button
              type='button'
              variant='outline'
              onClick={() =>
                props.change(props.rows.filter((r) => r.id !== row.id))
              }
            >
              {t('Delete row')}
            </Button>
          </div>
        </div>
      ))}
      <Button
        type='button'
        variant='outline'
        onClick={() =>
          props.change([...props.rows, newRow(grouped ? group : '')])
        }
      >
        {t('Add row')}
      </Button>
    </section>
  )
}
