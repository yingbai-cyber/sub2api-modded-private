/**
 * Admin Kiro OAuth API endpoints
 * Handles Kiro (AWS CodeWhisperer) OAuth flows for administrators:
 *   - Social login (GitHub/Google via app.kiro.dev)
 *   - AWS Builder ID / IAM Identity Center
 *   - External IdP (Microsoft Entra / Azure AD)
 *   - Token import (from Kiro IDE local storage)
 */

import { apiClient } from '../client'

// --- Types ---

export interface KiroAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
}

export interface KiroAuthUrlRequest {
  auth_method?: 'social' | 'external_idp'
  region?: string
  proxy_id?: number
  issuer_url?: string
  client_id?: string
  scopes?: string
}

export interface KiroIDCAuthUrlRequest {
  region?: string
  start_url?: string
  proxy_id?: number
}

export interface KiroExchangeCodeRequest {
  session_id: string
  state: string
  code: string
  proxy_id?: number
}

export interface KiroRefreshTokenRequest {
  refresh_token: string
  auth_method?: string
  region?: string
  client_id?: string
  client_secret?: string
  token_endpoint?: string
  scopes?: string
  proxy_id?: number
}

export interface KiroImportTokenRequest {
  token_json: string
  proxy_id?: number
}

export interface KiroTokenInfo {
  access_token?: string
  refresh_token?: string
  expires_in?: number
  expires_at?: string
  profile_arn?: string
  client_id?: string
  client_secret?: string
  auth_method?: string
}

export interface KiroExternalSSOStartResponse {
  session_id: string
  authorize_url: string
}

export interface KiroExternalSSOCallbackRequest {
  session_id: string
  callback_url: string
  proxy_id?: number
}

export interface KiroExternalSSOCallbackResponse {
  phase: 'need_open_url' | 'completed'
  authorize_url?: string
  token_info?: KiroTokenInfo
}

// --- API calls ---

/** Generate a social or external IdP authorization URL. */
export async function generateAuthUrl(
  payload: KiroAuthUrlRequest
): Promise<KiroAuthUrlResponse> {
  const { data } = await apiClient.post<KiroAuthUrlResponse>(
    '/admin/kiro/oauth/auth-url',
    payload
  )
  return data
}

/** Generate an AWS Builder ID / IDC authorization URL. */
export async function generateIDCAuthUrl(
  payload: KiroIDCAuthUrlRequest
): Promise<KiroAuthUrlResponse> {
  const { data } = await apiClient.post<KiroAuthUrlResponse>(
    '/admin/kiro/oauth/idc-auth-url',
    payload
  )
  return data
}

/** Exchange an authorization code for tokens. */
export async function exchangeCode(
  payload: KiroExchangeCodeRequest
): Promise<KiroTokenInfo> {
  const { data } = await apiClient.post<KiroTokenInfo>(
    '/admin/kiro/oauth/exchange-code',
    payload
  )
  return data
}

/** Validate a refresh token by performing a refresh. */
export async function refreshToken(
  payload: KiroRefreshTokenRequest
): Promise<KiroTokenInfo> {
  const { data } = await apiClient.post<KiroTokenInfo>(
    '/admin/kiro/oauth/refresh-token',
    payload
  )
  return data
}

/** Import a Kiro IDE token JSON. */
export async function importToken(
  payload: KiroImportTokenRequest
): Promise<KiroTokenInfo> {
  const { data } = await apiClient.post<KiroTokenInfo>(
    '/admin/kiro/oauth/import-token',
    payload
  )
  return data
}

/** Start a Microsoft Enterprise SSO two-leg flow. */
export async function startExternalSSO(
  payload?: { proxy_id?: number }
): Promise<KiroExternalSSOStartResponse> {
  const { data } = await apiClient.post<KiroExternalSSOStartResponse>(
    '/admin/kiro/oauth/external-sso/start',
    payload || {}
  )
  return data
}

/** Process a callback URL from the External SSO flow. */
export async function externalSSOCallback(
  payload: KiroExternalSSOCallbackRequest
): Promise<KiroExternalSSOCallbackResponse> {
  const { data } = await apiClient.post<KiroExternalSSOCallbackResponse>(
    '/admin/kiro/oauth/external-sso/callback',
    payload
  )
  return data
}
