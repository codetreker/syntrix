import { describe, expect, test } from 'bun:test';
import {
  businessEqual, canonicalJson, decodeBusinessPayload, definitionHash, encodeBusinessPayload,
  freezeSourceDefinition, frozenConditions, matchesConditions, projectDocument, recordKey,
  validateManifest, validateManifestIdentity, validateRecord, validateRecordIdentity,
} from './records.js';
import type { AliasManifest, DataRecord, ReplicaDocument, ReplicaSourceDefinition, MemberRecord } from './storage-types.js';

const data = async (id = 'alice'): Promise<DataRecord> => ({
  key: await recordKey('d', id), kind: 'd', logicalId: id, existence: 'live',
  payload: encodeBusinessPayload({ name: 'Alice', count: 9007199254740993n }),
  editToken: null, pin: null, wire: {},
});
const member = async (id = 'alice'): Promise<MemberRecord> => ({
  key: await recordKey('m', id), kind: 'm', logicalId: id, slots: [{ generation: 'g1', member: true }],
  observedExistence: 'live', metadata: { version: '5', createdAt: '10', updatedAt: '20' },
});
const manifest = async (): Promise<AliasManifest> => {
  const definition = freezeSourceDefinition({ collection: 'users', filters: [] });
  return {
    key: 'manifest', formatVersion: 1,
    namespace: { endpoint: 'https://example.test/prefix', subject: 'user', database: 'db', name: 'local', alias: 'users' },
    definition, definitionHash: await definitionHash(definition), boundDatabaseId: null, sourceHash: null,
    state: 'ready', activePhysicalEpoch: 'p1', physicalEpochs: ['p1'], maintenance: null,
    activeSourceGeneration: null, stagedSourceGeneration: null, sourceReady: false, partialDelivery: false,
    dirtyUpstream: null, issues: [], recoveryIntent: null,
  };
};
const document = (fields: Record<string, unknown>): ReplicaDocument => ({ id: 'alice', collection: 'users', ...fields }) as ReplicaDocument;

describe('canonical business records', () => {
  test('round trips typed nested fields with reserved envelope-like names and precision', () => {
    const source = {
      type: 'object', value: { _deleted: true, id: 'nested', nested: [{ precise: 9007199254740993n, float: 1.5 }, null] },
      _deleted: false, negative: -(1n << 63n), large: (1n << 63n) - 1n,
    };
    const encoded = encodeBusinessPayload(source);
    expect(decodeBusinessPayload(encoded)).toEqual(source);
    expect(encodeBusinessPayload({ b: 2, a: 1 })).toBe(encodeBusinessPayload({ a: 1, b: 2 }));
    expect(encodeBusinessPayload({ count: 1n })).not.toBe(encodeBusinessPayload({ count: 1 }));
    expect(canonicalJson({ 10: true, 2: false, nested: { z: 1, a: 2 } })).toBe('{"10":true,"2":false,"nested":{"a":2,"z":1}}');
  });

  test.each(['id', 'collection', 'version', 'createdAt', 'updatedAt', 'deleted'])('rejects protected top-level %s', field => {
    expect(() => encodeBusinessPayload({ [field]: 1 })).toThrow('read-only');
    const raw = JSON.stringify({ type: 'object', value: { [field]: { type: 'float64', value: 1 } } });
    expect(() => decodeBusinessPayload(raw)).toThrow();
  });

  test('rejects unsupported objects and corrupt or noncanonical persisted payloads', () => {
    for (const input of [undefined, [], null, new Date(), { bad: undefined }, { bad: Infinity }, { bad: 1n << 63n }, { [Symbol('bad')]: 1 }]) {
      expect(() => encodeBusinessPayload(input)).toThrow();
    }
    const payload = encodeBusinessPayload({ a: 1 });
    for (const input of ['garbage', '{"type":"array","value":[]}', ' ' + payload,
      '{"value":{"a":{"type":"float64","value":1}},"type":"object"}',
      '{"type":"object","value":{"a":{"type":"int64","value":"01"}}}',
      '{"type":"object","value":{},"value":{}}']) expect(() => decodeBusinessPayload(input)).toThrow();
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(() => canonicalJson(cyclic)).toThrow('cycles');
    for (const input of [1n, NaN, new Date(), { x: undefined }, '\ud800', { [Symbol()]: true }]) {
      expect(() => canonicalJson(input)).toThrow();
    }
  });

  test('uses fixed keys while preserving path segment identities including Unicode', async () => {
    const id = '艾丽丝😀';
    expect(await recordKey('d', id)).toMatch(/^d:[0-9a-f]{64}$/);
    expect((await recordKey('d', id)).slice(2)).toBe((await recordKey('m', id)).slice(2));
    const row = await data(id);
    await validateRecordIdentity(row);
    expect(projectDocument('users', row, null)?.id).toBe(id);
    await expect(validateRecordIdentity({ ...row, logicalId: 'other' })).rejects.toThrow('identity');
    for (const invalid of ['', 'a/b', '\0', '\udfff']) await expect(recordKey('d', invalid)).rejects.toThrow();
    await expect(recordKey('c' as 'd', 'alice')).rejects.toThrow();
  });

  test('projection uses desired existence but the latest source metadata', async () => {
    const d = await data();
    d.wire = { version: '1', updatedAt: '2' };
    const m = await member();
    const projected = projectDocument('users', d, m);
    expect(projected).toEqual({ id: 'alice', collection: 'users', name: 'Alice', count: 9007199254740993n, version: 5n, createdAt: 10n, updatedAt: 20n });
    expect(matchesConditions(projected, [{ field: 'version', op: '==', value: 5n }])).toBe(true);
    m.metadata.version = '6';
    expect(matchesConditions(projectDocument('users', d, m), [{ field: 'version', op: '==', value: 5n }])).toBe(false);
    expect(projectDocument('users', d, null)).toEqual({ id: 'alice', collection: 'users', name: 'Alice', count: 9007199254740993n });
    expect(projectDocument('users', { ...d, existence: 'deleted', payload: '' }, { ...m, metadata: {} })).toEqual({ id: 'alice', collection: 'users', deleted: true });
    expect(projectDocument('users', { ...d, existence: 'absent', payload: '' }, m)).toBeNull();
    expect(projectDocument('users', null, m)).toBeNull();
    expect(projectDocument('users', { ...d, payload: encodeBusinessPayload({ recreated: true }) }, m)).toMatchObject({ recreated: true, version: 6n });
    expect(() => projectDocument('users', d, { ...m, logicalId: 'bob' })).toThrow('identities');
  });

  test('business equality ignores local bookkeeping only for data records', async () => {
    const d = await data();
    expect(businessEqual(d, { ...d, editToken: 'e1', pin: { token: 'e1', stage: 'await-settlement' }, wire: { version: '2' } })).toBe(true);
    expect(businessEqual(d, { ...d, payload: encodeBusinessPayload({ count: 9007199254740992 }) })).toBe(false);
    const integer = { ...d, payload: encodeBusinessPayload({ count: 1n }) };
    expect(businessEqual(integer, { ...integer, payload: encodeBusinessPayload({ count: 1 }) })).toBe(false);
    expect(businessEqual(d, { ...d, existence: 'deleted', payload: '' })).toBe(false);
    expect(businessEqual(d, await data('bob'))).toBe(false);
    const m = await member();
    expect(businessEqual(m, { ...m, metadata: { version: '6' } })).toBe(false);
    expect(businessEqual(d, m)).toBe(false);
    expect(businessEqual(m, { ...m, _rev: '1-a', _meta: { lwt: 1 }, _deleted: false, _attachments: {} } as MemberRecord)).toBe(true);
    const c = { key: 'c:progress', kind: 'c', checkpoint: { after: 'alice' }, generation: 'g1', phase: 'scan', bootstrapComplete: false, partialDelivery: false } as const;
    expect(businessEqual(c, { ...c, checkpoint: { after: 'bob' } })).toBe(false);
    expect(businessEqual(c, { ...c })).toBe(true);
  });
});

describe('conditions and source definitions', () => {
  test('freezes detached conditions and validates the whole conjunction before matching', () => {
    const values = [1n, null];
    const input = [{ field: 'x', op: 'in' as const, value: values }];
    const frozen = frozenConditions(input);
    values.push(2n);
    input[0].field = 'y';
    expect(frozen[0]).toEqual({ field: 'x', op: 'in', value: [1n, null] });
    expect(Object.isFrozen(frozen[0].value)).toBe(true);
    expect(() => matchesConditions(null, [{ field: 'x', op: '==', value: 1 }, { field: 'bad.field', op: '==', value: 1 }])).toThrow();
    for (const condition of [
      { field: '', op: '==', value: 1 }, { field: 'a\n', op: '==', value: 1 },
      { field: 'a', op: 'wrong', value: 1 }, { field: 'a', op: 'in', value: 1 },
      { field: 'a', op: 'in', value: [{}] }, { field: 'a', op: '==', value: {} },
      { field: 'a', op: '>', value: true }, { field: 'a', op: 'contains', value: [] },
      { field: 'a', op: '==', value: 1, unknown: true },
    ]) expect(() => frozenConditions([condition as never])).toThrow();
    expect(() => frozenConditions(null as never)).toThrow();
  });

  test('distinguishes missing, null, types and exact numeric neighbours', () => {
    const doc = document({ integer: 9007199254740993n, float: 9007199254740992, nil: null, bool: true, values: [1, null, 'text'], object: {} });
    const match = (field: string, op: '==' | '!=' | '>' | '>=' | '<' | '<=' | 'in' | 'contains', value: never) =>
      matchesConditions(doc, [{ field, op, value }]);
    expect(match('missing', '!=', null as never)).toBe(false);
    expect(match('deleted', '==', false as never)).toBe(true);
    expect(matchesConditions(document({ deleted: true }), [{ field: 'deleted', op: '==', value: true }])).toBe(true);
    expect(matchesConditions(null, [{ field: 'deleted', op: '!=', value: true }])).toBe(false);
    expect(match('nil', '==', null as never)).toBe(true);
    expect(match('bool', '==', 1 as never)).toBe(false);
    expect(match('integer', '>', 9007199254740992 as never)).toBe(true);
    expect(match('integer', '<', 9007199254740994 as never)).toBe(true);
    expect(match('float', '==', 9007199254740993n as never)).toBe(false);
    expect(match('float', '==', 9007199254740992n as never)).toBe(true);
    expect(match('float', '<=', 9007199254740992n as never)).toBe(true);
    expect(match('integer', '>=', 9007199254740993n as never)).toBe(true);
    expect(match('integer', '>', '1' as never)).toBe(false);
    expect(match('nil', '!=', 0 as never)).toBe(true);
    expect(match('object', '==', null as never)).toBe(false);
    expect(match('values', 'contains', 1n as never)).toBe(true);
    expect(match('integer', 'contains', 1n as never)).toBe(false);
    expect(match('nil', 'in', ['x', null] as never)).toBe(true);
    expect(matchesConditions(document({ x: '\u{10000}' }), [{ field: 'x', op: '>', value: '\ue000' }])).toBe(true);
    expect(matchesConditions(document({ x: 'ab' }), [{ field: 'x', op: '>', value: 'a' }])).toBe(true);
    expect(matchesConditions(document({ x: 'a' }), [{ field: 'x', op: '>=', value: 'a' }])).toBe(true);
  });

  test('freezes a canonical typed definition with deterministic hash and effective order', async () => {
    const source: ReplicaSourceDefinition = { collection: 'teams/a/users', filters: [{ field: 'count', op: '>=', value: 9007199254740993n }], orderBy: [{ field: 'count', direction: 'desc' }], limit: 10 };
    const frozen = freezeSourceDefinition(source);
    expect(frozen.orderBy).toEqual([{ field: 'count', direction: 'desc' }, { field: 'id', direction: 'asc' }]);
    expect(frozen.filters[0].value).toEqual({ type: 'int64', value: '9007199254740993' });
    const hash = await definitionHash(frozen);
    source.filters[0].value = 1;
    expect(await definitionHash(frozen)).toBe(hash);
    expect(Object.isFrozen(frozen.filters[0].value)).toBe(true);
    expect(freezeSourceDefinition({ collection: 'users', filters: [], orderBy: [{ field: 'id', direction: 'desc' }] }).orderBy).toEqual([{ field: 'id', direction: 'desc' }]);
    expect(freezeSourceDefinition({ collection: 'users', filters: [] }).limit).toBeNull();
  });

  test('rejects invalid source structure instead of applying defaults', () => {
    const valid = { collection: 'users', filters: [] };
    for (const source of [null, { ...valid, extra: 1 }, { ...valid, collection: 'users/a' },
      { ...valid, collection: '/users' }, { ...valid, collection: 'users//x' },
      { ...valid, orderBy: null }, { ...valid, orderBy: [{ field: 'id', direction: 'up' }] },
      { ...valid, orderBy: [{ field: 'id', direction: 'asc', extra: true }] },
      { ...valid, orderBy: [{ field: 'id', direction: 'asc' }, { field: 'id', direction: 'desc' }] },
      ...[0, 1001, 0.5, null].map(limit => ({ ...valid, limit })),
    ]) expect(() => freezeSourceDefinition(source as never)).toThrow();
  });
});

describe('raw persistence validation', () => {
  test('rejects kind confusion, physical deletes, corrupt metadata and invalid bookkeeping', async () => {
    const d = await data();
    const bad = [null, { ...d, kind: 'unknown' }, { ...d, key: 'd:alice' }, { ...d, logicalId: 'a/b' },
      { ...d, extra: 1 }, { ...d, _deleted: true }, { ...d, _rev: '' }, { ...d, _meta: { lwt: Infinity } },
      { ...d, _attachments: { file: {} } }, { ...d, existence: 'wrong' }, { ...d, existence: 'deleted' },
      { ...d, payload: '{}' }, { ...d, wire: { version: '-1' } }, { ...d, wire: { version: '9223372036854775808' } },
      { ...d, wire: { updatedAt: '00' } }, { ...d, wire: { unknown: '1' } }, { ...d, wire: null },
      { ...d, pin: { token: 'wrong', stage: 'await-settlement' } },
      { ...d, editToken: 'e', pin: { token: 'e', stage: 'bad' } },
      { ...d, editToken: 'e', pin: { token: 'e', stage: 'await-source', settledRound: '' } },
    ];
    for (const input of bad) expect(() => validateRecord(input)).toThrow();
    validateRecord({ ...d, wire: { createdAt: '-9223372036854775808', updatedAt: '9223372036854775807' } });
    validateRecord({ ...d, editToken: 'e', pin: { token: 'e', stage: 'await-source', settledRound: 'round' } });
    const m = await member();
    for (const input of [{ ...m, slots: [m.slots[0], m.slots[0]] }, { ...m, slots: [1, 2, 3] },
      { ...m, slots: [{ generation: 'g', member: 'true' }] }, { ...m, observedExistence: false }]) expect(() => validateRecord(input)).toThrow();
    const c = { key: 'c:progress', kind: 'c', checkpoint: {}, generation: null, phase: 'scan', bootstrapComplete: false, partialDelivery: false };
    await validateRecordIdentity(c as never);
    for (const input of [{ ...c, key: 'c:other' }, { ...c, phase: '' }, { ...c, checkpoint: null }, { ...c, checkpoint: { bad: 1n } }]) expect(() => validateRecord(input)).toThrow();
  });

  test('validates manifest bindings, bounded generations, maintenance and markers', async () => {
    const base = await manifest();
    validateManifest(base);
    const busy = {
      ...base, boundDatabaseId: 'db-id', sourceHash: 'server-hash', activeSourceGeneration: 'g1', stagedSourceGeneration: 'g2',
      physicalEpochs: ['p1', 'p2'], maintenance: { id: 'maintenance', oldEpoch: 'p1', newEpoch: 'p2', stage: 'staging' },
      dirtyUpstream: { id: 'round', session: 1, physicalEpoch: 'p1', mayHaveDispatched: true, targets: [{ logicalId: 'alice', token: 'e1' }] },
      issues: [{ id: 'problem', logicalId: 'alice', code: 'conflict' }], recoveryIntent: null,
    };
    validateManifest(busy);
    validateManifest({ ...busy, activePhysicalEpoch: 'p2', maintenance: { ...busy.maintenance, stage: 'flipped' }, dirtyUpstream: null });
    const bad = [null, { ...base, formatVersion: 2 }, { ...base, namespace: { ...base.namespace, subject: '' } },
      { ...base, definition: null }, { ...base, definition: { ...base.definition, orderBy: [] } },
      { ...base, definition: { ...base.definition, filters: [{ field: 'x', op: '==', value: { type: 'bad' } }] } },
      { ...base, definitionHash: 'x' }, { ...base, boundDatabaseId: 'id' },
      { ...base, physicalEpochs: ['p1', 'p2', 'p3'] }, { ...base, physicalEpochs: ['p1', 'p1'] },
      { ...base, activePhysicalEpoch: 'other' }, { ...base, activeSourceGeneration: 'g', stagedSourceGeneration: 'g' },
      { ...busy, maintenance: { ...busy.maintenance, stage: 'flipped' } },
      { ...busy, dirtyUpstream: { ...busy.dirtyUpstream, physicalEpoch: 'other' } },
      { ...busy, dirtyUpstream: { ...busy.dirtyUpstream, targets: [{ logicalId: 'a/b', token: 'e' }] } },
      { ...busy, dirtyUpstream: { ...busy.dirtyUpstream, targets: [...busy.dirtyUpstream.targets, ...busy.dirtyUpstream.targets] } },
      { ...busy, dirtyUpstream: { ...busy.dirtyUpstream, targets: Array.from({ length: 201 }, (_, i) => ({ logicalId: `id-${i}`, token: 'edit' })) } },
      { ...busy, issues: Array.from({ length: 201 }, (_, i) => ({ id: `issue-${i}`, logicalId: null, code: 'uncertain' })) },
      { ...busy, issues: [{ id: 'issue', logicalId: null, code: 'uncertain', token: 1 }] },
      { ...busy, issues: [...busy.issues, ...busy.issues] }, { ...busy, issues: [{ id: 'a', code: 'b', logicalId: 'a/b' }] },
      { ...base, issues: null }, { ...base, recoveryIntent: [] },
    ];
    for (const input of bad) expect(() => validateManifest(input)).toThrow();
    validateManifest({ ...base, issues: [{ id: 'issue', logicalId: 'alice', code: 'conflict', token: null }] });
  });

  test('recovery intents preserve one validated target and two typed document states', async () => {
    const base = await manifest();
    await validateManifestIdentity(base);
    const current = { ...await data(), existence: 'absent' as const, payload: '' };
    const desired = { ...await data(), editToken: 'new-token', pin: { token: 'new-token', stage: 'await-settlement' as const } };
    const intent = { id: 'recovery-1', issueId: 'issue-1', phaseId: null, physicalEpoch: base.activePhysicalEpoch, action: 'merge' as const, logicalId: 'alice', protectedToken: 'old-token', resultToken: 'new-token', current, desired };
    await validateManifestIdentity({ ...base, recoveryIntent: intent });
    validateManifest({ ...base, recoveryIntent: { ...intent, action: 'adopt', protectedToken: null } });
    for (const invalid of [
      {}, { ...intent, logicalId: undefined }, { ...intent, logicalId: 'a/b' }, { ...intent, logicalId: 'bob' },
      { ...intent, issueId: '' }, { ...intent, phaseId: 1 }, { ...intent, physicalEpoch: 'other' },
      { ...intent, extra: true }, { ...intent, action: 'reset' }, { ...intent, resultToken: '' },
      { ...intent, protectedToken: 1 }, { ...intent, desired: { ...desired, editToken: 'other-token' } },
      { ...intent, current: { ...current, logicalId: 'bob' } },
      { ...intent, desired: { ...desired, payload: '{}' } }, { ...intent, current: await member() },
    ]) expect(() => validateManifest({ ...base, recoveryIntent: invalid })).toThrow();
    const otherKey = await recordKey('d', 'bob');
    await expect(validateManifestIdentity({ ...base, recoveryIntent: { ...intent, current: { ...current, key: otherKey }, desired: { ...desired, key: otherKey } } })).rejects.toThrow('identity');
    await expect(validateManifestIdentity({ ...base, definitionHash: '0'.repeat(64) })).rejects.toThrow('definition hash');
  });
});
