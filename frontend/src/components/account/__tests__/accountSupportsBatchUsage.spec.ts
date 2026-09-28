import { describe, expect, it } from 'vitest'
import { accountSupportsBatchUsage } from '../accountSupportsBatchUsage'

describe('accountSupportsBatchUsage', () => {
  it('includes native Kiro accounts so the desktop usage column can batch-load getUsageLimits', () => {
    expect(accountSupportsBatchUsage({ platform: 'anthropic', type: 'kiro' })).toBe(true)
  })

  it('keeps Anthropic OAuth and setup-token on the batch path', () => {
    expect(accountSupportsBatchUsage({ platform: 'anthropic', type: 'oauth' })).toBe(true)
    expect(accountSupportsBatchUsage({ platform: 'anthropic', type: 'setup-token' })).toBe(true)
  })

  it('does not batch Anthropic API keys or Bedrock rows', () => {
    expect(accountSupportsBatchUsage({ platform: 'anthropic', type: 'apikey' })).toBe(false)
    expect(accountSupportsBatchUsage({ platform: 'anthropic', type: 'bedrock' })).toBe(false)
  })
})
