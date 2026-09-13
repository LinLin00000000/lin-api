import { useTranslation } from 'react-i18next'
import { z } from 'zod'

// Mirrors pkg/identityservice.FrozenQuote, validating untrusted historical JSON.
// Never resolve against current settings or infer a missing value as zero/one.
const amount = z.number().finite().nonnegative()
const text = z.string().min(1)
const factor = z.object({ value: amount, source: text })
const frozenQuoteSchema = z.object({
  ratios: z.object({
    revision: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
    identity: text,
    service: text,
    physical_model: text,
    service_factor: factor,
    identity_factor: factor,
  }),
  billing_model: text,
  components: z
    .record(
      text,
      z.object({
        base: z.object({ value: amount, unit: text, source: text }),
        effective: amount,
      })
    )
    .refine((components) => Object.keys(components).length > 0),
  quota_per_unit: z.number().finite().positive(),
  rounding: text,
})

function Row(props: { label: string; value: string | number }) {
  return (
    <div className='grid min-w-0 grid-cols-2 gap-2 text-xs'>
      <dt className='text-muted-foreground'>{props.label}</dt>
      <dd className='min-w-0 font-mono break-all'>{String(props.value)}</dd>
    </div>
  )
}

export function FrozenBillingQuote(props: {
  quote: unknown
  expression?: boolean
}) {
  const { t } = useTranslation()
  const parsed = frozenQuoteSchema.safeParse(props.quote)
  const quote = parsed.success ? parsed.data : null
  return (
    <section
      aria-label={t('Frozen billing quote')}
      className='bg-muted/30 min-w-0 space-y-2 rounded-md border p-2.5'
    >
      <h3 className='text-xs font-semibold'>{t('Frozen billing quote')}</h3>
      <p className='text-muted-foreground text-xs'>
        {t('Historical snapshot only; current pricing is not used.')}
      </p>
      {!quote ? (
        <p className='text-xs'>{t('Unknown — not a free price')}</p>
      ) : (
        <>
          <dl className='space-y-1'>
            <Row label={t('Identity')} value={quote.ratios.identity} />
            <Row label={t('Service')} value={quote.ratios.service} />
            <Row label={t('Revision')} value={quote.ratios.revision} />
            <Row
              label={t('Physical model')}
              value={quote.ratios.physical_model}
            />
            <Row label={t('Billing model')} value={quote.billing_model} />
            <Row
              label={t('Service factor S')}
              value={quote.ratios.service_factor.value}
            />
            <Row
              label={t('Service factor source')}
              value={quote.ratios.service_factor.source}
            />
            <Row
              label={t('Identity factor D')}
              value={quote.ratios.identity_factor.value}
            />
            <Row
              label={t('Identity factor source')}
              value={quote.ratios.identity_factor.source}
            />
            <Row label={t('Quota per unit')} value={quote.quota_per_unit} />
            <Row label={t('Rounding')} value={quote.rounding} />
          </dl>
          <p className='text-muted-foreground text-xs'>
            {t('Frozen estimates and multipliers are not final charges.')}
          </p>
          {Object.entries(quote.components).map(([name, component]) => (
            <div key={name} className='min-w-0 space-y-1 border-t pt-2'>
              <h4 className='font-mono text-xs break-all'>{name}</h4>
              <dl className='space-y-1'>
                <Row label={t('Base price B')} value={component.base.value} />
                <Row
                  label={
                    props.expression || name === 'multiplier'
                      ? t('Frozen effective value')
                      : t('Effective unit price')
                  }
                  value={component.effective}
                />
                <Row label={t('Unit')} value={component.base.unit} />
                <Row
                  label={t('Base price source')}
                  value={component.base.source}
                />
              </dl>
            </div>
          ))}
        </>
      )}
    </section>
  )
}
