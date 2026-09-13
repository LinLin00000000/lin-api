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
import { useMutation, useQuery } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { useId, useLayoutEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  readIdentityService,
  saveIdentityService,
  validateIdentityService,
  type IdentityServiceConfig,
  type ValidationReport,
} from './api'
import { toConfig, toDraft, type Draft, type Section } from './draft'
import { DraftSection } from './draft-section'

function errorMessage(error: unknown) {
  if (isAxiosError(error)) return error.response?.data?.message || error.message
  return error instanceof Error
    ? error.message
    : 'Identity/service request failed'
}
type Auth = ReturnType<typeof useAuthStore.getState>['auth']
// Conservative local ownership: even same-ID user/permission replacement ends a draft.
// Tokens/session objects stay in memory, never in React keys or query metadata.
function sameOwner(a: Auth, b: Auth) {
  return (
    a.user === b.user &&
    a.accessToken === b.accessToken &&
    a.session === b.session
  )
}
export function IdentityServiceSection() {
  const user = useAuthStore((s) => s.auth.user)
  const [generation, setGeneration] = useState(0)
  useLayoutEffect(
    () =>
      useAuthStore.subscribe((next, previous) => {
        if (!sameOwner(next.auth, previous.auth)) {
          setGeneration((value) => value + 1)
        }
      }),
    []
  )
  // RootAuth is the existing boundary, not the channel capability matrix.
  if (user?.role !== ROLE.SUPER_ADMIN) return null
  return <RootEditor key={`${user.id}:${generation}`} userId={user.id} />
}
function RootEditor(props: { userId: number }) {
  const { t } = useTranslation()
  const instance = useId()
  const query = useQuery({
    queryKey: ['identity-service-setting', props.userId, instance],
    queryFn: () => readIdentityService(),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  if (query.isPending) return <p role='status'>{t('Loading settings...')}</p>
  if (query.isError) {
    return (
      <div role='alert'>
        <p>{t('Could not load identity/service settings.')}</p>
        <p>{t(errorMessage(query.error))}</p>
        <Button onClick={() => void query.refetch()}>{t('Retry')}</Button>
      </div>
    )
  }
  return <Editor initial={query.data} />
}
function Editor(props: { initial: IdentityServiceConfig }) {
  const { t } = useTranslation()
  const [base, setBase] = useState(props.initial)
  const form = useForm<Draft>({ defaultValues: toDraft(props.initial) })
  const draft = form.watch()
  const [report, setReport] = useState<ValidationReport | null>(null)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)
  const saveAttempted = useRef(false)
  const owner = useRef({
    controller: new AbortController(),
    alive: false,
    generation: 0,
    auth: useAuthStore.getState().auth,
  })
  useLayoutEffect(() => {
    const lifetime = owner.current
    lifetime.controller = new AbortController()
    lifetime.alive = true
    const unsubscribe = useAuthStore.subscribe((next, previous) => {
      if (!sameOwner(next.auth, previous.auth)) {
        lifetime.controller.abort()
        lifetime.alive = false
        lifetime.generation += 1
      }
    })
    return () => {
      lifetime.controller.abort()
      lifetime.alive = false
      lifetime.generation += 1
      unsubscribe()
    }
  }, [])
  const [confirmReload, setConfirmReload] = useState(false)
  const replace = (config: IdentityServiceConfig) => {
    setBase(config)
    form.reset(toDraft(config))
    setBlocked(false)
  }
  const operation = useMutation({
    retry: false,
    mutationFn: async (action: 'validate' | 'save' | 'reload') => {
      const generation = owner.current.generation
      const signal = owner.current.controller.signal
      const isCurrent = () =>
        owner.current.alive &&
        owner.current.generation === generation &&
        sameOwner(owner.current.auth, useAuthStore.getState().auth)
      const assertOwner = () => {
        if (!isCurrent()) {
          throw new Error('Editor operation no longer owns this session')
        }
      }
      if (!isCurrent()) return
      try {
        saveAttempted.current = false
        setError('')
        setMessage('')
        setReport(null)
        if (action === 'reload') {
          const config = await readIdentityService(signal)
          assertOwner()
          replace(config)
          setConfirmReload(false)
          return
        }
        const config = toConfig(base, form.getValues())
        const result = await validateIdentityService(config, signal)
        assertOwner()
        setReport(result)
        if (result?.success !== true || result.config_valid !== true) {
          throw new Error(
            result?.message || 'Draft validation failed; nothing was saved.'
          )
        }
        if (action === 'save') {
          saveAttempted.current = true
          const saved = await saveIdentityService(config, assertOwner, signal)
          assertOwner()
          replace(saved)
          setMessage('Saved legacy configuration and verified server readback.')
        } else setMessage('Structure valid. Activation remains blocked.')
      } catch (cause) {
        if (!isCurrent()) return
        if (isAxiosError(cause) && cause.response?.data?.activation_blockers) {
          setReport(cause.response.data)
        }
        const conflict = isAxiosError(cause) && cause.response?.status === 409
        // A failed PUT may have committed despite transport loss. Never retry it automatically.
        if (action === 'save' && saveAttempted.current) setBlocked(true)
        setError(
          conflict
            ? 'Revision conflict. Draft kept. Compare your changes, then explicitly discard and reload before saving.'
            : errorMessage(cause)
        )
      }
    },
  })
  const change = (section: Section, rows: Draft[Section]) => {
    form.setValue(section, rows, { shouldDirty: true })
    setReport(null)
    setMessage('')
    setError('')
  }
  return (
    <div className='space-y-4 pb-6'>
      <div className='bg-muted space-y-2 rounded-lg border p-4 text-sm'>
        <h2 className='font-medium'>
          {t('Identity and service configuration')}
        </h2>
        <p>
          {t(
            'Inactive: legacy mode. Saving this draft does not enable identity/service billing.'
          )}
        </p>
        <p>
          {t(
            'Missing overrides inherit defaults. 0 is free; 1 explicitly means no discount. Delete an override to restore inheritance.'
          )}
        </p>
        <p>
          {t(
            'Missing or closed service models are not open. Missing eligibility denies access. D overrides change price, never eligibility.'
          )}
        </p>
        <p>
          {t(
            'Exact keys are preserved. The dedicated validator decides group, identity and model consistency.'
          )}
        </p>
        <p>
          {t('Configuration revision')}: {base.revision}
        </p>
      </div>
      {error && (
        <div role='alert' className='text-destructive space-y-1'>
          <p>{t(error)}</p>
          <p>{t('Draft kept. No successful save is claimed.')}</p>
        </div>
      )}
      {message && <p role='status'>{t(message)}</p>}
      {report && (
        <div
          aria-label={t('Validation result')}
          className='space-y-2 rounded-lg border p-4 text-sm'
        >
          <p>
            {t(
              'Activation not ready. These safety blockers are expected in inactive mode.'
            )}
          </p>
          <ul className='list-inside list-disc break-words'>
            {[
              ...new Set([
                ...(report.activation_blockers ?? []),
                ...(report.pending_verification ?? []),
              ]),
            ].map((item) => (
              <li key={item}>{t(item)}</li>
            ))}
          </ul>
        </div>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault()
          if (!blocked && !operation.isPending) operation.mutate('save')
        }}
      >
        <fieldset disabled={operation.isPending} className='min-w-0 space-y-4'>
          {base.migration_source_digest && (
            <div className='space-y-2 rounded-lg border p-4 text-sm'>
              <p>
                {t(
                  'This configuration has migration provenance. Manual edits require explicitly discarding that proof; they do not certify migration.'
                )}
              </p>
              <Button
                type='button'
                variant='outline'
                onClick={() => {
                  setBase({ ...base, migration_source_digest: '' })
                  setReport(null)
                  setMessage('')
                }}
              >
                {t('Discard migration proof for manual draft')}
              </Button>
            </div>
          )}
          {(Object.keys(draft) as Section[]).map((section) => (
            <DraftSection
              key={section}
              section={section}
              rows={draft[section]}
              groups={
                section === 'service_models'
                  ? draft.service_defaults.map((r) => r.key)
                  : draft.identity_defaults.map((r) => r.key)
              }
              change={(rows) => change(section, rows)}
            />
          ))}
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              variant='outline'
              onClick={() => operation.mutate('validate')}
            >
              {t('Validate draft')}
            </Button>
            <Button type='submit' disabled={blocked}>
              {t('Validate and save legacy draft')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() => setConfirmReload(true)}
            >
              {t('Discard draft and reload')}
            </Button>
          </div>
          {confirmReload && (
            <div
              role='group'
              aria-label={t('Confirm draft discard')}
              className='flex flex-wrap items-center gap-2 rounded-lg border p-3'
            >
              <p>{t('Reload discards all unsaved groups and overrides.')}</p>
              <Button type='button' onClick={() => operation.mutate('reload')}>
                {t('Confirm discard and reload')}
              </Button>
              <Button
                type='button'
                variant='outline'
                onClick={() => setConfirmReload(false)}
              >
                {t('Cancel')}
              </Button>
            </div>
          )}
        </fieldset>
      </form>
      {operation.isPending && (
        <p role='status'>{t('Checking server configuration...')}</p>
      )}
    </div>
  )
}
