import { describe, expect, it } from 'bun:test';
import { decodeQueryValue, encodeQueryValue } from './value';

describe('Query value codec', () => {
  it('preserves int64 neighbors, bounds, and float64 types through JSON', () => {
    const values = [-(1n << 63n), -9007199254740993n, 9007199254740992n, 9007199254740993n, (1n << 63n) - 1n, 1, 1n, Number.MIN_VALUE];
    expect(decodeQueryValue(JSON.parse(JSON.stringify(encodeQueryValue(values))))).toEqual(values);
    expect(encodeQueryValue(1)).toEqual({ type: 'float64', value: 1 });
    expect(encodeQueryValue(1n)).toEqual({ type: 'int64', value: '1' });
    expect(Object.is(decodeQueryValue({ type: 'float64', value: -0 }), 0)).toBe(true);
  });

  it('round trips objects that resemble wire nodes and prototype keys', () => {
    const value = JSON.parse('{"type":"int64","value":"9007199254740993","__proto__":{"constructor":true},"empty":[],"nil":null}');
    const decoded = decodeQueryValue(JSON.parse(JSON.stringify(encodeQueryValue(value)))) as Record<string, unknown>;
    expect(decoded).toEqual(value);
    expect(Object.prototype.hasOwnProperty.call(decoded, '__proto__')).toBe(true);
    expect(Object.getPrototypeOf(decoded)).toBe(Object.prototype);
    expect(decodeQueryValue(encodeQueryValue({ nested: [{ type: 'null' }], text: '𐀀' }))).toEqual({ nested: [{ type: 'null' }], text: '𐀀' });
  });

  it.each([
    NaN, Infinity, -Infinity, 1n << 63n, -(1n << 63n) - 1n,
    undefined, () => 1, Symbol('value'), new Date(), new Map(),
    { nested: undefined }, { [Symbol('key')]: 1 }, [undefined], Array(1), '\ud800', { '\udc00': 1 },
  ].map(value => ({ value })))('rejects unsupported input %#', ({ value }) => {
    expect(() => encodeQueryValue(value)).toThrow();
  });

  it('rejects cycles but permits repeated objects', () => {
    const cycle: unknown[] = [];
    cycle.push(cycle);
    expect(() => encodeQueryValue(cycle)).toThrow('cycles');
    const shared = { n: 1n };
    expect(decodeQueryValue(encodeQueryValue([shared, shared]))).toEqual([shared, shared]);
  });

  it.each([
    null, [], 1, {}, { type: 'null', value: null }, { type: 'null', extra: 1 },
    { type: 'int64' }, { type: 'int64', value: 1 }, { type: 'int64', value: '01' },
    { type: 'int64', value: '-0' }, { type: 'int64', value: '+1' },
    { type: 'int64', value: '9223372036854775808' },
    { type: 'float64', value: '1.0' }, { type: 'float64', value: Infinity },
    { type: 'bool', value: 0 }, { type: 'string', value: null }, { type: 'string', value: '\ud800' },
    { type: 'array', value: {} }, { type: 'array', value: [1] },
    { type: 'object', value: [] }, { type: 'object', value: { n: 1 } },
    { type: 'unknown', value: 'x' },
  ].map(value => ({ value })))('rejects malformed wire value %#', ({ value }) => {
    expect(() => decodeQueryValue(value)).toThrow();
  });
});
