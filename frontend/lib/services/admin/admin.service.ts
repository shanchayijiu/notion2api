import { apiFetch, assertAPIResponse, buildAPIURL, readSSEMessages } from '@/lib/services/core/api-client';
import type {
  AccountsPayload,
  AdminConfigPayload,
  AdminVerifyPayload,
  AttachmentInput,
  ConversationDetailPayload,
  ConversationsPayload,
  HealthPayload,
  JsonResult,
  VersionPayload,
  WorkspacesPayload,
} from './types';

export const AdminService = {
  verify() {
    return apiFetch<AdminVerifyPayload>('/admin/verify');
  },
  login(password: string) {
    return apiFetch<JsonResult>('/admin/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
  },
  logout() {
    return apiFetch<JsonResult>('/admin/logout', {
      method: 'POST',
    });
  },
  getConfig() {
    return apiFetch<AdminConfigPayload>('/admin/config');
  },
  getVersion() {
    return apiFetch<VersionPayload>('/admin/version');
  },
  getHealth() {
    return apiFetch<HealthPayload>('/healthz');
  },
  getAccounts() {
    return apiFetch<AccountsPayload>('/admin/accounts');
  },
  getWorkspaces() {
    return apiFetch<WorkspacesPayload>('/admin/workspaces');
  },
  rotateWorkspace(accountEmail: string) {
    return apiFetch<JsonResult>('/admin/workspaces/rotate', {
      method: 'POST',
      body: JSON.stringify({ account_email: accountEmail }),
    });
  },
  deleteWorkspace(spaceId: string) {
    return apiFetch<JsonResult>('/admin/workspaces/delete', {
      method: 'POST',
      body: JSON.stringify({ space_id: spaceId }),
    });
  },
  getConversations() {
    return apiFetch<ConversationsPayload>('/admin/conversations');
  },
  getConversation(id: string) {
    return apiFetch<ConversationDetailPayload>(`/admin/conversations/${encodeURIComponent(id)}`);
  },
  deleteConversation(id: string) {
    return apiFetch<JsonResult>(`/admin/conversations/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },
  batchDeleteConversations(ids: string[]) {
    return apiFetch<JsonResult>('/admin/conversations/batch-delete', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    });
  },
  async runWireTest(payload: {
    prompt: string;
    model: string;
    use_web_search: boolean;
    attachments: AttachmentInput[];
    conversation_id?: string;
    stream?: boolean;
    api_key?: string;
    signal?: AbortSignal;
    onEvent?: (event: { event: string; data: string }) => void;
  }) {
    const { api_key, signal, onEvent, stream = true, ...body } = payload;
    const keyPayload = api_key ? { api_key } : await apiFetch<{ api_key: string }>('/admin/test/key');
    const response = await fetch(buildAPIURL('/v1/chat/completions'), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${keyPayload.api_key || ''}`,
      },
      credentials: 'include',
      signal,
      body: JSON.stringify({
        model: body.model,
        messages: [{ role: 'user', content: body.prompt }],
        stream,
        use_web_search: body.use_web_search,
        attachments: body.attachments,
        conversation_id: body.conversation_id,
      }),
    });
    await assertAPIResponse(response);
    if (!stream) return response.json() as Promise<JsonResult>;
    const startedAt = performance.now();
    let firstByteMs: number | undefined;
    const events: Array<{ event: string; data: string }> = [];
    const textParts: string[] = [];
    const reasoningParts: string[] = [];
    const toolCalls: Record<string, { id?: string; name?: string; arguments: string }> = {};
    let usage: unknown;
    await readSSEMessages(response, (event) => {
      firstByteMs ??= performance.now() - startedAt;
      events.push(event);
      onEvent?.(event);
      if (event.data === '[DONE]') return;
      try {
        const payload = JSON.parse(event.data) as {
          choices?: Array<{ delta?: { content?: string; reasoning_content?: string; tool_calls?: Array<{ index?: number; id?: string; function?: { name?: string; arguments?: string } }> } }>;
          usage?: unknown;
          error?: { message?: string };
        };
        if (payload.error?.message) throw new Error(payload.error.message);
        const delta = payload.choices?.[0]?.delta;
        if (delta?.content) textParts.push(delta.content);
        if (delta?.reasoning_content) reasoningParts.push(delta.reasoning_content);
        for (const call of delta?.tool_calls || []) {
          const key = String(call.index ?? Object.keys(toolCalls).length);
          const current = toolCalls[key] || { arguments: '' };
          current.id ||= call.id;
          current.name ||= call.function?.name;
          current.arguments += call.function?.arguments || '';
          toolCalls[key] = current;
        }
        if (payload.usage) usage = payload.usage;
      } catch (error) {
        if (error instanceof Error && error.message) throw error;
      }
    });
    return {
      stream: true,
      first_byte_ms: firstByteMs == null ? null : Math.round(firstByteMs),
      text: textParts.join(''),
      reasoning: reasoningParts.join(''),
      tool_calls: Object.values(toolCalls),
      usage,
      events,
    } as JsonResult;
  },

  updateSettings(config: JsonResult) {
    return apiFetch<JsonResult>('/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({ config }),
    });
  },
  importConfig(config: JsonResult) {
    return apiFetch<JsonResult>('/admin/config/import', {
      method: 'POST',
      body: JSON.stringify({ config }),
    });
  },
  exportConfig() {
    return apiFetch<JsonResult>('/admin/config/export');
  },
  createConfigSnapshot() {
    return apiFetch<JsonResult>('/admin/config/snapshot', { method: 'POST' });
  },
  listConfigSnapshots() {
    return apiFetch<JsonResult>('/admin/config/snapshot');
  },
  startAccountLogin(email: string) {
    return apiFetch<JsonResult>('/admin/accounts/login/start', {
      method: 'POST',
      body: JSON.stringify({ email }),
    });
  },
  verifyAccountCode(email: string, code: string) {
    return apiFetch<JsonResult>('/admin/accounts/login/verify', {
      method: 'POST',
      body: JSON.stringify({ email, code }),
    });
  },
  importAccount(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts/manual', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  quickTestAccount(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts/test', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  registerAccount(proxy?: string) {
    const query = proxy && proxy.trim() ? `?proxy=${encodeURIComponent(proxy.trim())}` : '';
    return apiFetch<JsonResult>(`/admin/accounts/register${query}`, {
      method: 'POST',
    });
  },
  activateAccount(email: string) {
    return apiFetch<JsonResult>('/admin/accounts/activate', {
      method: 'POST',
      body: JSON.stringify({ email }),
    });
  },
  deleteAccount(email: string) {
    return apiFetch<JsonResult>(`/admin/accounts/${encodeURIComponent(email)}`, {
      method: 'DELETE',
    });
  },
  saveAccountSettings(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts', {
      method: 'PUT',
      body: JSON.stringify(payload),
    });
  },
};
