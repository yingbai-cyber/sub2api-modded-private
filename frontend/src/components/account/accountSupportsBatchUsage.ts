import type { Account } from '@/types'

/** Desktop account-list usage column: types the parent may load via POST /usage/batch. */
export function accountSupportsBatchUsage(account: Pick<Account, 'platform' | 'type'>): boolean {
  if (account.platform === 'anthropic') {
    return account.type === 'oauth' || account.type === 'setup-token' || account.type === 'kiro'
  }
  if (account.platform === 'gemini') return true
  if (account.platform === 'antigravity') return account.type === 'oauth'
  if (account.platform === 'openai') return account.type === 'oauth'
  if (account.platform === 'grok') return account.type === 'oauth'
  return false
}
