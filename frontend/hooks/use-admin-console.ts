'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AdminService } from '@/lib/services/admin/admin.service';
import { buildEventStreamURL } from '@/lib/services/core/api-client';
import type {
  AccountsPayload,
  AdminConfigPayload,
  AdminVerifyPayload,
  ConversationDetail,
  ConversationMessage,
  ConversationSummary,
  HealthPayload,
  JsonResult,
  VersionPayload,
} from '@/lib/services/admin/types';

function sortConversations(items: ConversationSummary[]): ConversationSummary[] {
  return [...items].sort((left, right) => {
    const leftTime = new Date(left.updated_at || left.created_at || 0).getTime();
    const rightTime = new Date(right.updated_at || right.created_at || 0).getTime();
    return rightTime - leftTime;
  });
}

function mergeConversationSummaryState(
  current: ConversationSummary | undefined,
  next: ConversationSummary,
): ConversationSummary {
  if (!current) return next;
  const preserveMergedTitle =
    current.origin === 'merged' &&
    current.thread_id &&
    current.thread_id === next.thread_id &&
    (!next.origin || next.origin === 'local');
  return {
    ...current,
    ...next,
    title: preserveMergedTitle ? current.title || next.title : next.title || current.title,
    origin: next.origin || current.origin,
    remote_only: next.remote_only ?? current.remote_only,
    created_by_display_name: next.created_by_display_name || current.created_by_display_name,
  };
}

function mergeConversationMessages(
  currentMessages: ConversationMessage[] = [],
  nextMessage?: ConversationMessage,
): ConversationMessage[] {
  if (!nextMessage?.id) return currentMessages;
  const next = [...currentMessages];
  const index = next.findIndex((item) => item.id === nextMessage.id);
  if (index >= 0) {
    next[index] = nextMessage;
  } else {
    next.push(nextMessage);
  }
  return next;
}

export function useAdminConsole() {
  const [verify, setVerify] = useState<AdminVerifyPayload | null>(null);
  const [configPayload, setConfigPayload] = useState<AdminConfigPayload | null>(null);
  const [versionPayload, setVersionPayload] = useState<VersionPayload | null>(null);
  const [healthPayload, setHealthPayload] = useState<HealthPayload | null>(null);
  const [accountsPayload, setAccountsPayload] = useState<AccountsPayload | null>(null);
  const [conversations, setConversations] = useState<ConversationSummary[]>([]);
  const [conversationDetails, setConversationDetails] = useState<Record<string, ConversationDetail>>({});
  const [selectedConversationId, setSelectedConversationId] = useState('');
  const [streamState, setStreamState] = useState('实时流未连接');
  const [bootLoading, setBootLoading] = useState(true);
  const [bootError, setBootError] = useState('');
  const eventSourceRef = useRef<EventSource | null>(null);
  const selectedConversationIdRef = useRef('');
  const conversationDetailsRef = useRef<Record<string, ConversationDetail>>({});

  const upsertConversationSummary = useCallback((summary?: ConversationSummary | null) => {
    if (!summary?.id) return;
    setConversations((current) => {
      const existing = current.find((item) => item.id === summary.id);
      const filtered = current.filter((item) => item.id !== summary.id);
      filtered.push(mergeConversationSummaryState(existing, summary));
      return sortConversations(filtered);
    });
  }, []);

  const rememberConversation = useCallback((conversation?: ConversationDetail | null) => {
    if (!conversation?.id) return;
    setConversationDetails((current) => {
      const next = {
        ...current,
        [conversation.id]: conversation,
      };
      conversationDetailsRef.current = next;
      return next;
    });
  }, []);

  const closeStream = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
      eventSourceRef.current = null;
    }
    setStreamState('实时流未连接');
  }, []);

  const loadConversationDetail = useCallback(async (conversationId: string, force = false) => {
    if (!conversationId) {
      setSelectedConversationId('');
      return;
    }
    setSelectedConversationId(conversationId);
    if (!force && conversationDetailsRef.current[conversationId]) {
      return;
    }
    const payload = await AdminService.getConversation(conversationId);
    rememberConversation(payload.item);
  }, [rememberConversation]);

  const loadConversations = useCallback(async () => {
    const payload = await AdminService.getConversations();
    const items = sortConversations(payload.items || []);
    setConversations(items);
    setSelectedConversationId((current) => (
      current && items.some((item) => item.id === current) ? current : items[0]?.id || ''
    ));
    return items;
  }, []);

  const refreshConversations = useCallback(async () => {
    const items = await loadConversations();
    const currentID = selectedConversationIdRef.current;
    const targetID = currentID && items.some((item) => item.id === currentID) ? currentID : items[0]?.id || '';
    if (targetID) {
      await loadConversationDetail(targetID, true);
    }
    return items;
  }, [loadConversationDetail, loadConversations]);

  const refreshAll = useCallback(async () => {
    setBootLoading(true);
    setBootError('');
    try {
      const verifyPayload = await AdminService.verify();
      setVerify(verifyPayload);

      if (!verifyPayload.password_configured || (!verifyPayload.authenticated && verifyPayload.password_required)) {
        closeStream();
        setConfigPayload(null);
        setVersionPayload(null);
        setHealthPayload(null);
        setAccountsPayload(null);
        setConversations([]);
        setConversationDetails({});
        setSelectedConversationId('');
        return;
      }

      const [config, version, health, accounts] = await Promise.all([
        AdminService.getConfig(),
        AdminService.getVersion(),
        AdminService.getHealth(),
        AdminService.getAccounts(),
      ]);
      setConfigPayload(config);
      setVersionPayload(version);
      setHealthPayload(health);
      setAccountsPayload(accounts);
      await refreshConversations();
    } catch (error) {
      setBootError(error instanceof Error ? error.message : '加载控制台失败');
    } finally {
      setBootLoading(false);
    }
  }, [closeStream, refreshConversations]);

  const ensureStream = useCallback(() => {
    if (eventSourceRef.current || !verify?.authenticated) return;
    const source = new EventSource(buildEventStreamURL('/admin/events'), { withCredentials: true });
    eventSourceRef.current = source;
    setStreamState('实时流连接中');

    source.onopen = () => setStreamState('实时流已连接');
    source.onerror = () => setStreamState('实时流重连中');

    const handleEvent = (payload: {
      type?: string;
      summary?: ConversationSummary;
      conversation?: ConversationDetail;
      conversation_id?: string;
      message?: ConversationMessage;
      at?: string;
    }) => {
      if (payload.summary) upsertConversationSummary(payload.summary);
      if (payload.conversation) rememberConversation(payload.conversation);

      const conversationID = payload.conversation_id;

      if (payload.type === 'conversation.delta' && conversationID) {
        setConversationDetails((current) => {
          const existing = current[conversationID];
          if (!existing) return current;
          return {
            ...current,
            [conversationID]: {
              ...existing,
              status: payload.summary?.status || existing.status,
              updated_at: payload.summary?.updated_at || payload.at || existing.updated_at,
              messages: mergeConversationMessages(existing.messages, payload.message),
            },
          };
        });
      }

      if (payload.type === 'conversation.created' && conversationID) {
        setSelectedConversationId((current) => current || conversationID || '');
      }
      if ((payload.type === 'conversation.completed' || payload.type === 'conversation.failed') && conversationID) {
        setSelectedConversationId((current) => current || conversationID || '');
      }
    };

    ['admin.ready', 'conversation.created', 'conversation.updated', 'conversation.delta', 'conversation.completed', 'conversation.failed'].forEach((eventName) => {
      source.addEventListener(eventName, (event) => {
        if (!event.data) return;
        try {
          const payload = JSON.parse(event.data);
          if (eventName === 'admin.ready') {
            setStreamState('实时流已连接');
            return;
          }
          handleEvent(payload);
        } catch (error) {
          console.error('failed to parse admin event', error);
        }
      });
    });
  }, [rememberConversation, upsertConversationSummary, verify?.authenticated]);

  useEffect(() => {
    selectedConversationIdRef.current = selectedConversationId;
  }, [selectedConversationId]);

  useEffect(() => {
    conversationDetailsRef.current = conversationDetails;
  }, [conversationDetails]);

  useEffect(() => {
    void refreshAll();
    return () => closeStream();
  }, [closeStream, refreshAll]);

  useEffect(() => {
    if (verify?.authenticated) {
      ensureStream();
    } else {
      closeStream();
    }
  }, [closeStream, ensureStream, verify?.authenticated]);

  useEffect(() => {
    if (!selectedConversationId) return;
    if (!conversations.some((item) => item.id === selectedConversationId)) {
      setSelectedConversationId(conversations[0]?.id || '');
    }
  }, [conversations, selectedConversationId]);

  const selectedConversation = useMemo(
    () => (selectedConversationId ? conversationDetails[selectedConversationId] || conversations.find((item) => item.id === selectedConversationId) || null : null),
    [conversationDetails, conversations, selectedConversationId],
  );

  const login = useCallback(async (password: string) => {
    const result = await AdminService.login(password);
    await refreshAll();
    return result;
  }, [refreshAll]);

  const logout = useCallback(async () => {
    const result = await AdminService.logout();
    await refreshAll();
    return result;
  }, [refreshAll]);

  const deleteConversation = useCallback(async (conversationId: string) => {
    const result = await AdminService.deleteConversation(conversationId);
    setConversationDetails((current) => {
      const next = { ...current };
      delete next[conversationId];
      return next;
    });
    await refreshConversations();
    return result;
  }, [refreshConversations]);

  const batchDeleteConversations = useCallback(async (ids: string[]) => {
    const result = await AdminService.batchDeleteConversations(ids);
    setConversationDetails((current) => {
      const next = { ...current };
      ids.forEach((id) => delete next[id]);
      return next;
    });
    await refreshConversations();
    return result;
  }, [refreshConversations]);

  const refreshAccounts = useCallback(async () => {
    const payload = await AdminService.getAccounts();
    setAccountsPayload(payload);
    return payload;
  }, []);

  const refreshConfigBundle = useCallback(async () => {
    const [config, version, health] = await Promise.all([
      AdminService.getConfig(),
      AdminService.getVersion(),
      AdminService.getHealth(),
    ]);
    setConfigPayload(config);
    setVersionPayload(version);
    setHealthPayload(health);
    return { config, version, health };
  }, []);

  return {
    verify,
    configPayload,
    versionPayload,
    healthPayload,
    accountsPayload,
    conversations,
    conversationDetails,
    selectedConversation,
    selectedConversationId,
    streamState,
    bootLoading,
    bootError,
    setSelectedConversationId,
    loadConversationDetail,
    loadConversations,
    refreshAll,
    refreshConversations,
    refreshAccounts,
    refreshConfigBundle,
    login,
    logout,
    deleteConversation,
    batchDeleteConversations,
    closeStream,
    services: AdminService,
  };
}
