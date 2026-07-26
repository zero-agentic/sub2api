export interface ILuminaStoredCookieInput {
  name: string;
  value: string;
  domain: string;
  path: string;
  expires?: string;
  max_age?: number;
  secure: boolean;
  http_only: boolean;
  partitioned: boolean;
  host_only: boolean;
}

export function parseLuminaCookieJarJSON(input: string): ILuminaStoredCookieInput[] {
  if (!input.trim()) {
    return [];
  }
  const parsed: unknown = JSON.parse(input);
  if (!Array.isArray(parsed)) {
    throw new Error('cookie jar must be an array');
  }
  const cookies: ILuminaStoredCookieInput[] = [];
  for (const value of parsed) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error('cookie entry must be an object');
    }
    const cookie = value as Record<string, unknown>;
    const name = typeof cookie.name === 'string' ? cookie.name.trim() : '';
    const cookieValue = typeof cookie.value === 'string' ? cookie.value : '';
    const domain = typeof cookie.domain === 'string' ? cookie.domain.trim() : '';
    if (!name || !domain) {
      throw new Error('cookie name and domain are required');
    }
    // 真实浏览器导出常见空 value 条目，过滤而非整体拒绝
    if (!cookieValue) {
      continue;
    }
    const normalized: ILuminaStoredCookieInput = {
      name,
      value: cookieValue,
      domain,
      path: typeof cookie.path === 'string' && cookie.path.startsWith('/') ? cookie.path : '/',
      secure: cookie.secure === true,
      http_only: cookie.http_only === true || cookie.httpOnly === true,
      partitioned: cookie.partitioned === true,
      host_only: cookie.host_only === true || cookie.hostOnly === true,
    };
    const expires = cookie.expires ?? cookie.expirationDate;
    if (typeof expires === 'number' && Number.isFinite(expires) && expires > 0) {
      // Chrome expirationDate 为秒级时间戳，部分工具导出毫秒级，按量级区分
      normalized.expires = new Date(expires > 1e12 ? expires : expires * 1000).toISOString();
    } else if (typeof expires === 'string' && expires.trim()) {
      // 字符串 expires 常见 HTTP-date（后端只认 RFC3339），统一归一为 ISO
      const parsedExpires = Date.parse(expires.trim());
      if (Number.isNaN(parsedExpires)) {
        throw new Error(`cookie "${name}" has an unparsable expires value`);
      }
      normalized.expires = new Date(parsedExpires).toISOString();
    }
    if (!normalized.expires) {
      const maxAge = cookie.max_age ?? cookie.maxAge;
      if (typeof maxAge === 'number' && Number.isInteger(maxAge) && maxAge > 0) {
        normalized.max_age = maxAge;
      }
    }
    cookies.push(normalized);
  }
  if (parsed.length > 0 && cookies.length === 0) {
    throw new Error('cookie jar contains no usable entries');
  }
  return cookies;
}
