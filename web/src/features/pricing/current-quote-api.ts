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
import { z } from 'zod'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

const number = z.number().finite().nonnegative()
const factor = z.object({ value: number, source: z.string().min(1) })
const component = z.object({
  base: z.object({ value: number, unit: z.string().min(1), source: z.string().min(1) }),
  effective: number,
})
const components = z.custom<Record<string, unknown>>(
  (value) => value !== null && typeof value === 'object' && !Array.isArray(value)
).transform((value) => Object.entries(value))
  .pipe(z.array(z.tuple([z.string(), component])).min(1))
  .transform((entries) => Object.fromEntries(entries))
const quoteResponse = z.discriminatedUnion('mode', [
  z.object({ success: z.literal(true), mode: z.literal('legacy'), preview: z.literal(false) }),
  z.object({
    success: z.literal(true), mode: z.literal('identity_service'), preview: z.boolean(), kind: z.literal('current_quote'),
    data: z.object({
      calculator: z.enum(['synchronous', 'expression']),
      final_price_known: z.literal(false),
      expression: z.string().optional(),
      conditions: z.array(z.string()),
      quote: z.object({
        ratios: z.object({ revision: number, identity: z.string(), service: z.string(), physical_model: z.string(), service_factor: factor, identity_factor: factor }),
        billing_model: z.string(), components, quota_per_unit: number.positive(), rounding: z.string(),
      }),
    }),
  }),
])
export type CurrentQuoteResponse = z.infer<typeof quoteResponse>

export class CurrentQuoteAuthenticationExpiredError extends Error {
  readonly code = 'CURRENT_QUOTE_AUTH_EXPIRED'
  constructor() { super('Current quote authentication expired') }
}

export async function readCurrentQuote(model: string, service: string, previewIdentity: string | undefined, signal: AbortSignal, expectedIdentity: string | undefined): Promise<CurrentQuoteResponse> {
  const owner = useAuthStore.getState().auth
  const preview = previewIdentity !== undefined
  if (!owner.user || !expectedIdentity || (!preview && owner.user.group !== expectedIdentity)) {
    throw new Error('Current quote identity unavailable')
  }
  let response
  try {
    response = await api.get(preview ? '/api/option/identity_service/quote' : '/api/pricing/current', {
      params: { model, service, ...(preview ? { identity: previewIdentity } : {}) },
      signal, disableDuplicate: true, skipAuthRefresh: true, skipBusinessError: true, skipErrorHandler: true,
    })
  } catch (error: any) {
    const current = useAuthStore.getState().auth
    if (error?.response?.status === 401 && owner.user === current.user && owner.accessToken === current.accessToken && owner.session === current.session) {
      throw new CurrentQuoteAuthenticationExpiredError()
    }
    throw error
  }
  const current = useAuthStore.getState().auth
  if (signal.aborted || owner.user !== current.user || owner.accessToken !== current.accessToken || owner.session !== current.session) {
    throw new Error('Current quote session changed')
  }
  const result = quoteResponse.parse(response.data)
  if (result.mode === 'identity_service') {
    const ratios = result.data.quote.ratios
    if (ratios.physical_model !== model || ratios.service !== service || result.preview !== preview || ratios.identity !== expectedIdentity) {
      throw new Error('Current quote selection mismatch')
    }
  }
  return result
}
