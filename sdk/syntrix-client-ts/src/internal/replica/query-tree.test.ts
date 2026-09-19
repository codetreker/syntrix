import { describe, expect, test } from 'bun:test';
import { OrderedQueryTree } from './query-tree.js';
import { compareOrderKeys, makeOrderKey, normalizeReplicaQuery, type ReplicaOrderKey } from './query-semantics.js';

describe('ordered AVL candidate tree', () => {
  test.each([{ input: [3, 2, 1] }, { input: [1, 2, 3] }, { input: [3, 1, 2] }, { input: [1, 3, 2] }])('balances insertion rotations %p', ({ input }) => {
    const tree = new OrderedQueryTree<number, string>((a, b) => a - b);
    for (const key of input) tree.insert(key, String(key));
    expect(tree.height).toBe(2);
    expect([...tree.entries()]).toEqual([[1, '1'], [2, '2'], [3, '3']]);
    expect(tree.size).toBe(3);
    tree.insert(2, 'replacement');
    expect(tree.get(2)).toBe('replacement');
    expect(tree.size).toBe(3);
    expect(tree.get(9)).toBeUndefined();
  });

  test('matches an independent sorted-map oracle across randomized insert/remove/seek', () => {
    const tree = new OrderedQueryTree<number, number>((a, b) => a - b);
    const oracle = new Map<number, number>();
    let state = 123456789;
    const random = () => { state = (Math.imul(state, 1664525) + 1013904223) >>> 0; return state; };
    for (let step = 0; step < 6000; step++) {
      const id = random() % 1000;
      if (random() % 3) { tree.insert(id, step); oracle.set(id, step); }
      else expect(tree.remove(id)).toBe(oracle.delete(id));
      if (step % 37 === 0) {
        const expected = [...oracle.entries()].sort(([a], [b]) => a - b);
        expect([...tree.entries()]).toEqual(expected);
        expect(tree.size).toBe(oracle.size);
        expect(tree.height).toBeLessThanOrEqual(Math.ceil(1.45 * Math.log2(tree.size + 2)));
        const after = random() % 1000;
        expect([...tree.entries(after)]).toEqual(expected.filter(([key]) => key > after));
      }
    }
    for (const id of oracle.keys()) expect(tree.remove(id)).toBe(true);
    expect(tree.size).toBe(0);
    expect(tree.height).toBe(0);
    expect(tree.remove(123)).toBe(false);
    tree.insert(1, 1);
    tree.clear();
    expect([...tree.entries()]).toEqual([]);
  });

  test('remains logarithmic under monotonic input and deletion', () => {
    const tree = new OrderedQueryTree<number, number>((a, b) => a - b);
    for (let i = 0; i < 10000; i++) tree.insert(i, i);
    expect(tree.height).toBeLessThan(20);
    for (let i = 0; i < 9990; i++) tree.remove(i);
    expect([...tree.entries()].map(([key]) => key)).toEqual(Array.from({ length: 10 }, (_, i) => i + 9990));
    expect(tree.height).toBeLessThan(6);
  });

  test('keeps complete candidates for exact mixed-value window replacement', async () => {
    const query = await normalizeReplicaQuery({ orderBy: [{ field: 'score', direction: 'asc' }] }, 'scope');
    const tree = new OrderedQueryTree<ReplicaOrderKey, string>((a, b) => compareOrderKeys(a, b, query.orderBy));
    const source = [
      { id: 'a', collection: 'items', score: 9007199254740993n },
      { id: 'b', collection: 'items', score: 9007199254740992 },
      { id: 'c', collection: 'items', score: '\ue000' },
      { id: 'd', collection: 'items', score: '\u{10000}' },
    ];
    const keys = source.map(row => makeOrderKey(row, query.orderBy));
    keys.forEach(key => tree.insert(key, key.id));
    const top = () => [...tree.entries()].slice(0, 2).map(([, id]) => id);
    expect(top()).toEqual(['b', 'a']);
    tree.remove(keys[1]);
    expect(top()).toEqual(['a', 'c']);
    tree.remove(keys[3]);
    const replacement = makeOrderKey({ ...source[3], score: -1n }, query.orderBy);
    tree.insert(replacement, 'd');
    expect(top()).toEqual(['d', 'a']);
    expect([...tree.entries(keys[0])].map(([, id]) => id)).toEqual(['c']);
  });
});
