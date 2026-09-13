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
import { useQuery } from '@tanstack/react-query'
import { useId, useLayoutEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { readIdentityService } from '@/features/system-settings/billing/identity-service/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { CurrentQuoteAuthenticationExpiredError, readCurrentQuote, type CurrentQuoteResponse } from '../current-quote-api'
import { clearAuthentication } from '@/lib/auth-session'

function QuoteValues(props: { response: CurrentQuoteResponse }) {
  const { t } = useTranslation()
  if (props.response.mode === 'legacy') {
    return <p>{t('Legacy pricing is active. Identity/service pricing is not enabled.')}</p>
  }
  const data = props.response.data
  const quote = data.quote
  const ratios = quote.ratios
  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-xs'>{t('Current unit prices, not a historical bill. Request usage and additional ratios still apply.')}</p>
      {props.response.preview && <p className='text-amber-700 dark:text-amber-300'>{t('Saved configuration preview only. Does not enable billing or grant access.')}</p>}
      <dl className='grid gap-2 text-sm sm:grid-cols-2'>
        <div><dt>{t('Identity')}</dt><dd>{ratios.identity}</dd></div>
        <div><dt>{t('Revision')}</dt><dd>{ratios.revision}</dd></div>
        <div><dt>{t('Service factor (S)')}</dt><dd>{ratios.service_factor.value} · <code>{ratios.service_factor.source}</code></dd></div>
        <div><dt>{t('Identity factor (D)')}</dt><dd>{ratios.identity_factor.value} · <code>{ratios.identity_factor.source}</code></dd></div>
      </dl>
      <p className='text-muted-foreground text-xs'>{t('Revision identifies identity/service configuration; base prices are read again on each query.')}</p>
      <p className='text-muted-foreground text-xs'>{t('Synchronous calculator projection only; does not certify Task, MJ or custom monetary billing.')}</p>
      {data.expression && <div><p>{t('Final price unknown until actual request and usage are available.')}</p><code className='block overflow-auto whitespace-pre-wrap break-all'>{data.expression}</code></div>}
      <div className='overflow-x-auto rounded-lg border'>
        <table className='w-full text-left text-xs'>
          <thead><tr><th className='p-2'>{t('Component')}</th><th className='p-2'>{t('Base price (B)')}</th><th className='p-2'>{t('Effective unit price')}</th><th className='p-2'>{t('Unit')}</th><th className='p-2'>{t('Source')}</th></tr></thead>
          <tbody>{Object.entries(quote.components).map(([name, item]) => <tr key={name} className='border-t'>
            <th className='p-2 font-normal'>{name}</th><td className='p-2 font-mono'>{item.base.value}</td><td className='p-2 font-mono'>{item.effective}</td><td className='p-2'>{item.base.unit}</td><td className='p-2 break-all'>{item.base.source}</td>
          </tr>)}</tbody>
        </table>
      </div>
      <p className='text-muted-foreground text-xs'>{t('Zero is an explicit component price, not an unknown price or a waiver of other fees.')}</p>
    </div>
  )
}

export function CurrentQuoteSection(props: { model: string; services: string[] }) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  // Recovery must compare the store's epoch, not this component's local counter.
  const authGeneration = useAuthStore((state) => state.auth.generation)
  const [open, setOpen] = useState(false)
  const [preview, setPreview] = useState(false)
  const [identity, setIdentity] = useState('')
  const [selectedService, setService] = useState('')
  const instance = useId()
  const [generation, setGeneration] = useState(0)
  // Like the C1 editor, auth replacement ends ownership. Keep secrets out of
  // query keys; a local generation also isolates same-id logout/login cycles.
  useLayoutEffect(() => useAuthStore.subscribe((next, previous) => {
    if (next.auth.user !== previous.auth.user || next.auth.accessToken !== previous.auth.accessToken || next.auth.session !== previous.auth.session) {
      setGeneration((value) => value + 1)
      setPreview(false)
      setIdentity('')
    }
  }), [])
  const isRoot = user?.role === ROLE.SUPER_ADMIN
  const isPreview = Boolean(isRoot && preview)
  const config = useQuery({
    queryKey: ['current-quote-preview-config', instance, generation, user?.id, user?.role, user?.group],
    queryFn: ({ signal }) => readIdentityService(signal),
    enabled: open && isPreview,
    retry: false, staleTime: 0, gcTime: 0,
  })
  const services = isPreview ? Object.keys(config.data?.service_defaults ?? {}) : props.services
  const service = services.includes(selectedService) ? selectedService : ''
  const previewIdentity = isPreview ? identity : undefined
  const expectedIdentity = isPreview ? identity : user?.group
  const enabled = open && Boolean(user && service && expectedIdentity)
  const query = useQuery({
    queryKey: ['current-quote', instance, generation, user?.id, user?.role, user?.group, props.model, service, previewIdentity],
    queryFn: ({ signal }) => readCurrentQuote(props.model, service, previewIdentity, signal, expectedIdentity),
    enabled, retry: false, staleTime: 0, gcTime: 0,
  })
  if (!user) return null
  return (
    <section className='space-y-3 rounded-lg border p-3'>
      <Button variant='outline' size='sm' aria-expanded={open} onClick={() => setOpen(!open)}>{t('Current identity/service quote')}</Button>
      {open && <div className='space-y-3'>
        {isRoot && <label className='flex items-center gap-2 text-sm'><input type='checkbox' checked={preview} onChange={(event) => { setPreview(event.target.checked); setService(''); setIdentity('') }} />{t('Preview saved identity/service configuration')}</label>}
        {isPreview && <label className='block space-y-1 text-sm'><span>{t('Preview identity')}</span><select className='bg-background w-full rounded-md border p-2' value={identity} onChange={(event) => setIdentity(event.target.value)}><option value=''>{t('Select identity')}</option>{Object.keys(config.data?.identity_defaults ?? {}).map((id) => <option key={id} value={id}>{id}</option>)}</select></label>}
        <label className='block space-y-1 text-sm'><span>{t('Quote service')}</span><select className='bg-background w-full rounded-md border p-2' value={service} onChange={(event) => setService(event.target.value)}><option value=''>{t('Select service')}</option>{services.map((group) => <option key={group} value={group}>{group}</option>)}</select></label>
        {isPreview && config.isFetching && <p role='status'>{t('Loading...')}</p>}
        {query.error instanceof CurrentQuoteAuthenticationExpiredError && <div role='alert' className='space-y-2'><p>{t('Your session expired while loading this quote.')}</p><Button variant='outline' size='sm' onClick={() => { const auth = useAuthStore.getState().auth; if (auth.user === user && auth.generation === authGeneration) { clearAuthentication(false); const redirect = `${window.location.pathname}${window.location.search}`; window.location.replace(`/sign-in?redirect=${encodeURIComponent(redirect)}`) } }}>{t('Sign in again')}</Button></div>}
        {(query.isError && !(query.error instanceof CurrentQuoteAuthenticationExpiredError) || (isPreview && config.isError)) && <p role='alert'>{t('Current quote unavailable. Unknown is not free.')}</p>}
        {enabled && query.isFetching && <p role='status'>{t('Loading...')}</p>}
        {enabled && !query.isFetching && !query.isError && query.data && <QuoteValues response={query.data} />}
        <Button variant='outline' size='sm' disabled={!enabled || query.isFetching} onClick={() => void query.refetch()}>{t('Refresh current quote')}</Button>
      </div>}
    </section>
  )
}
