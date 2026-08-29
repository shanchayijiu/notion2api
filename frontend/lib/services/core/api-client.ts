import type { AttachmentInput } from '@/lib/services/admin/types';

const API_BASE = (process.env.NEXT_PUBLIC_BACKEND_BASE_URL || '').replace(/\/$/, '');

function buildURL(path: string): string {
  return `${API_BASE}${path}`;
}

export function buildEventStreamURL(path: string): string {
  return buildURL(path);
}

function summarizeHTMLText(raw: string): string {
  const text = String(raw || '').trim();
  const lower = text.toLowerCase();
  if (!(lower.startsWith('<!doctype html') || lower.startsWith('<html') || lower.includes('<title'))) {
    return '';
  }
  const titleMatch = text.match(/<title[^>]*>([\s\S]*?)<\/title>/i);
  const h1Match = text.match(/<h1[^>]*>([\s\S]*?)<\/h1>/i);
  const strip = (value: string) => value.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
  const title = strip(titleMatch?.[1] || '');
  const h1 = strip(h1Match?.[1] || '');
  if (title && h1 && title !== h1) return `上游返回 HTML 错误页: ${title} | ${h1}`;
  if (title) return `上游返回 HTML 错误页: ${title}`;
  if (h1) return `上游返回 HTML 错误页: ${h1}`;
  return '上游返回了 HTML 错误页';
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers || {});
  if (init?.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const response = await fetch(buildURL(path), {
    ...init,
    headers,
    credentials: 'include',
  });
  const contentType = response.headers.get('content-type') || '';
  const hitNation2API = response.headers.get('x-notion2api') === '1';
  const payload = contentType.includes('application/json') ? await response.json() : await response.text();

  if (!response.ok) {
    const htmlSummary = typeof payload === 'string' ? summarizeHTMLText(payload) : '';
    if (htmlSummary && !hitNation2API) {
      throw new Error(`当前响应未命中 nation2api（status ${response.status} ${response.statusText}，url ${response.url}），而是前置代理/反代返回的 HTML 错误页: ${htmlSummary.replace(/^上游返回 HTML 错误页:\s*/, '')}`);
    }
    if (typeof payload === 'object' && payload !== null) {
      const detail = (payload as { detail?: string; error?: { message?: string } }).detail;
      const message = (payload as { detail?: string; error?: { message?: string } }).error?.message;
      throw new Error(detail || message || `${response.status} ${response.statusText}`);
    }
    throw new Error(htmlSummary || String(payload || `${response.status} ${response.statusText}`));
  }

  return payload as T;
}

export async function readFilesAsAttachments(files: File[]): Promise<AttachmentInput[]> {
  return Promise.all(
    files.map(
      (file) =>
        new Promise<AttachmentInput>((resolve, reject) => {
          const reader = new FileReader();
          reader.onload = () =>
            resolve({
              type: 'attachment',
              name: file.name,
              content_type: file.type,
              data: reader.result,
            });
          reader.onerror = () => reject(reader.error || new Error('读取文件失败'));
          reader.readAsDataURL(file);
        }),
    ),
  );
}

export async function copyText(text: string): Promise<void> {
  await navigator.clipboard.writeText(text);
}
