export type QueryValue = null | boolean | string | number | bigint | QueryValue[] | { [key: string]: QueryValue };

export type TypedValue =
  | { type: 'null' }
  | { type: 'bool'; value: boolean }
  | { type: 'string'; value: string }
  | { type: 'int64'; value: string }
  | { type: 'float64'; value: number }
  | { type: 'array'; value: TypedValue[] }
  | { type: 'object'; value: { [key: string]: TypedValue } };

const minInt64 = -(1n << 63n);
const maxInt64 = (1n << 63n) - 1n;

const validString = (value: string): string => {
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(++index);
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new TypeError('Query string contains an unpaired surrogate');
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      throw new TypeError('Query string contains an unpaired surrogate');
    }
  }
  return value;
};

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const finiteNumber = (value: number): number => {
  if (!Number.isFinite(value)) throw new TypeError('Query numbers must be finite');
  return value === 0 ? 0 : value;
};

const int64 = (value: bigint): bigint => {
  if (value < minInt64 || value > maxInt64) throw new RangeError('Query integer is outside the int64 range');
  return value;
};

/** Encodes bigint as int64 and number as finite float64; unsupported values fail. */
export const encodeQueryValue = (value: unknown): TypedValue => {
  const ancestors = new Set<object>();
  const encode = (item: unknown): TypedValue => {
    if (item === null) return { type: 'null' };
    switch (typeof item) {
      case 'boolean': return { type: 'bool', value: item };
      case 'string': return { type: 'string', value: validString(item) };
      case 'bigint': return { type: 'int64', value: int64(item).toString() };
      case 'number': return { type: 'float64', value: finiteNumber(item) };
      case 'object': {
        if (ancestors.has(item)) throw new TypeError('Query values must not contain cycles');
        ancestors.add(item);
        try {
          if (Array.isArray(item)) {
            return { type: 'array', value: Array.from(item, encode) };
          }
          const prototype = Object.getPrototypeOf(item);
          if (prototype !== Object.prototype && prototype !== null) {
            throw new TypeError('Query objects must be plain objects');
          }
          if (Object.getOwnPropertySymbols(item).length !== 0) {
            throw new TypeError('Query objects must have string keys');
          }
          return {
            type: 'object',
            value: Object.fromEntries(Object.entries(item).map(([key, child]) => [validString(key), encode(child)])),
          };
        } finally {
          ancestors.delete(item);
        }
      }
      default: throw new TypeError(`Unsupported query value type: ${typeof item}`);
    }
  };
  return encode(value);
};

/** Decodes every int64 as bigint and rejects malformed typed values. */
export const decodeQueryValue = (node: unknown): QueryValue => {
  if (!isObject(node) || Object.keys(node).some(key => key !== 'type' && key !== 'value')) {
    throw new TypeError('Query value must be a typed value object');
  }
  const hasValue = Object.prototype.hasOwnProperty.call(node, 'value');
  if (node.type === 'null') {
    if (hasValue) throw new TypeError('Null query value must omit value');
    return null;
  }
  if (!hasValue) throw new TypeError('Typed query value requires value');
  const value = node.value;
  switch (node.type) {
    case 'bool':
      if (typeof value === 'boolean') return value;
      break;
    case 'string':
      if (typeof value === 'string') return validString(value);
      break;
    case 'int64':
      if (typeof value === 'string' && /^(0|-?[1-9][0-9]*)$/.test(value)) return int64(BigInt(value));
      break;
    case 'float64':
      if (typeof value === 'number') return finiteNumber(value);
      break;
    case 'array':
      if (Array.isArray(value)) return value.map(decodeQueryValue);
      break;
    case 'object':
      if (isObject(value)) {
        return Object.fromEntries(Object.entries(value).map(([key, child]) => [validString(key), decodeQueryValue(child)]));
      }
      break;
  }
  throw new TypeError(`Invalid typed query value: ${String(node.type)}`);
};
