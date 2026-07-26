import { describe, expect, it } from 'vitest';

import { parseLuminaCookieJarJSON } from '../lumina-credentials';

describe('parseLuminaCookieJarJSON', () => {
  it('normalizes Chrome cookie exports for backend storage', () => {
    const cookies = parseLuminaCookieJarJSON(JSON.stringify([{
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      path: '/',
      secure: true,
      httpOnly: true,
      expirationDate: 1_900_000_000,
    }]));

    expect(cookies).toEqual([expect.objectContaining({
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      http_only: true,
      expires: new Date(1_900_000_000 * 1000).toISOString(),
    })]);
  });

  it('rejects non-array and incomplete cookie payloads', () => {
    expect(() => parseLuminaCookieJarJSON('{}')).toThrow();
    expect(() => parseLuminaCookieJarJSON('[{"name":"sid"}]')).toThrow();
  });

  it('preserves a relative max age when no absolute expiry is available', () => {
    const cookies = parseLuminaCookieJarJSON(JSON.stringify([{
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      maxAge: 3600,
    }]));

    expect(cookies[0]).toEqual(expect.objectContaining({ max_age: 3600 }));
    expect(cookies[0]).not.toHaveProperty('expires');
  });

  it('normalizes HTTP-date string expires to ISO format', () => {
    const cookies = parseLuminaCookieJarJSON(JSON.stringify([{
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      expires: 'Wed, 21 Oct 2026 07:28:00 GMT',
    }]));

    expect(cookies[0].expires).toBe('2026-10-21T07:28:00.000Z');
  });

  it('treats millisecond-level numeric expires as milliseconds', () => {
    const ms = 1_900_000_000_000;
    const cookies = parseLuminaCookieJarJSON(JSON.stringify([{
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      expirationDate: ms,
    }]));

    expect(cookies[0].expires).toBe(new Date(ms).toISOString());
  });

  it('rejects string expires values that cannot be parsed', () => {
    expect(() => parseLuminaCookieJarJSON(JSON.stringify([{
      name: 'sessionid',
      value: 'secret',
      domain: '.byteplus.com',
      expires: 'not-a-date',
    }]))).toThrow(/expires/);
  });

  it('drops empty-value cookie entries instead of rejecting the whole jar', () => {
    const cookies = parseLuminaCookieJarJSON(JSON.stringify([
      { name: 'empty_hint', value: '', domain: '.byteplus.com' },
      { name: 'sessionid', value: 'secret', domain: '.byteplus.com' },
    ]));

    expect(cookies).toHaveLength(1);
    expect(cookies[0]).toEqual(expect.objectContaining({ name: 'sessionid' }));
  });

  it('rejects a jar where every entry has an empty value', () => {
    expect(() => parseLuminaCookieJarJSON(JSON.stringify([
      { name: 'empty_hint', value: '', domain: '.byteplus.com' },
    ]))).toThrow();
  });
});
