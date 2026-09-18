import { describe, expect, test } from 'bun:test';
import { readBoundedChanges, validateBoundedReadOptions } from './bounded-reader';

type Row = { id: string; body: string; order: number };
type Cursor = { position: number };

const fixture = (lengths: number[]) => {
  let rows: Row[] = lengths.map((length, index) => ({
    id: String(index), body: 'x'.repeat(length), order: index + 1,
  }));
  const calls: { limit: number; checkpoint: Cursor | undefined }[] = [];
  const read = async (limit: number, checkpoint: Cursor | undefined) => {
    calls.push({ limit, checkpoint });
    const documents = rows.filter((row) => row.order > (checkpoint?.position ?? 0)).slice(0, limit);
    return {
      documents,
      checkpoint: { position: documents[documents.length - 1]?.order ?? checkpoint?.position ?? 0 },
    };
  };
  const update = (id: string, length: number) => {
    const order = Math.max(...rows.map((row) => row.order)) + 1;
    rows = [...rows.filter((row) => row.id !== id), { id, body: 'x'.repeat(length), order }];
  };
  return { read, calls, update };
};

const limits = { targetBytes: 650, maxDocumentBytes: 2048 };

describe('bounded changed-document reads', () => {
  test('clamps native batches to 50 rows using 13 bounded reads', async () => {
    const source = fixture(Array(60).fill(1));
    const page = await readBoundedChanges(source.read, undefined, 100);
    expect(page.documents).toHaveLength(50);
    expect(page.checkpoint).toEqual({ position: 50 });
    expect(source.calls.map((call) => call.limit)).toEqual([...Array(12).fill(4), 2]);
  });

  test('honors a smaller requested limit and custom read blocks', async () => {
    const source = fixture(Array(10).fill(1));
    const page = await readBoundedChanges(source.read, undefined, 3, { readBlockDocuments: 2 });
    expect(page.documents).toHaveLength(3);
    expect(source.calls.map((call) => call.limit)).toEqual([2, 1]);
  });

  test('rereads a fitting prefix without skipping unreturned rows', async () => {
    const source = fixture([220, 220, 220, 220, 220, 220]);
    let checkpoint: Cursor | undefined;
    const ids: string[] = [];
    for (let index = 0; index < 4; index++) {
      const page = await readBoundedChanges(source.read, checkpoint, 50, limits);
      ids.push(...page.documents.map((row) => row.id));
      checkpoint = page.checkpoint;
    }
    expect(ids).toEqual(['0', '1', '2', '3', '4', '5']);
    expect(source.calls[0]).toEqual({ limit: 4, checkpoint: undefined });
    expect(source.calls[1]).toEqual({ limit: 2, checkpoint: undefined });
    expect(checkpoint).toEqual({ position: 6 });
  });

  test('recomputes admission when rows change during a prefix reread', async () => {
    const source = fixture([220, 220, 220, 220, 220, 220]);
    let mutated = false;
    const read = async (limit: number, checkpoint: Cursor | undefined) => {
      if (!mutated && limit === 2) {
        mutated = true;
        source.update('1', 1300);
      }
      return source.read(limit, checkpoint);
    };
    let checkpoint: Cursor | undefined;
    const rows: Row[] = [];
    for (let index = 0; index < 10; index++) {
      const page = await readBoundedChanges(read, checkpoint, 50, limits);
      if (page.documents.length === 0) break;
      const size = page.documents.reduce((sum, row) => sum + new TextEncoder().encode(JSON.stringify(row)).length, 0);
      expect(size <= limits.targetBytes || page.documents.length === 1).toBe(true);
      expect(page.checkpoint?.position).toBe(page.documents[page.documents.length - 1]?.order);
      checkpoint = page.checkpoint;
      rows.push(...page.documents);
    }
    expect(mutated).toBe(true);
    expect(rows.map((row) => row.id)).toEqual(['0', '2', '3', '4', '5', '1']);
    expect(rows[rows.length - 1]?.body.length).toBe(1300);
  });

  test('recomputes a prefix that grows beyond the remaining page budget', async () => {
    const source = fixture([100, 100, 100, 100, 100, 100]);
    let mutated = false;
    const read = async (limit: number, checkpoint: Cursor | undefined) => {
      if (checkpoint?.position === 4 && limit === 1 && !mutated) {
        mutated = true;
        source.update('4', 1300);
        source.update('5', 1300);
      }
      return source.read(limit, checkpoint);
    };
    const page = await readBoundedChanges(read, undefined, 50, limits);
    expect(mutated).toBe(true);
    expect(page.documents.map((row) => row.id)).toEqual(['0', '1', '2', '3']);
    expect(page.checkpoint).toEqual({ position: 4 });
  });

  test('admits a legal oversized singleton with its own checkpoint', async () => {
    const source = fixture([1300, 100]);
    const page = await readBoundedChanges(source.read, undefined, 50, limits);
    expect(page.documents.map((row) => row.id)).toEqual(['0']);
    expect(page.checkpoint).toEqual({ position: 1 });
    expect(source.calls.map((call) => call.limit)).toEqual([4, 1]);
  });

  test('rejects an oversized row without publishing a partial checkpoint', async () => {
    const source = fixture([100, 3000]);
    let checkpoint: Cursor | undefined;
    await expect((async () => {
      const page = await readBoundedChanges(source.read, checkpoint, 50, limits);
      checkpoint = page.checkpoint;
    })()).rejects.toThrow('encoded byte limit');
    expect(checkpoint).toBeUndefined();
  });

  test('measures UTF-8 bytes instead of string length', async () => {
    await expect(readBoundedChanges(async () => ({ documents: ['💾'], checkpoint: 1 }), undefined, 1,
      { targetBytes: 10, maxDocumentBytes: 5 })).rejects.toThrow('encoded byte limit');
  });

  test('empty reads retain the input checkpoint even if the source advances it', async () => {
    const read = async () => ({ documents: [] as Row[], checkpoint: { position: 99 } });
    expect(await readBoundedChanges(read, undefined, 50)).toEqual({ documents: [], checkpoint: undefined });
    const checkpoint = { position: 7 };
    expect((await readBoundedChanges(read, checkpoint, 50)).checkpoint).toBe(checkpoint);
  });

  test('propagates storage errors unchanged', async () => {
    const error = new Error('disk unavailable');
    await expect(readBoundedChanges(async () => { throw error; }, undefined, 50)).rejects.toBe(error);
  });

  test('rejects a source exceeding the requested block', async () => {
    await expect(readBoundedChanges(async () => ({ documents: [1, 2], checkpoint: 2 }), undefined, 1))
      .rejects.toThrow('requested limit');
  });

  test('rejects unserializable rows without coercion', async () => {
    for (const value of [undefined, 1n]) {
      await expect(readBoundedChanges(async () => ({ documents: [value], checkpoint: 1 }), undefined, 1))
        .rejects.toBeInstanceOf(TypeError);
    }
  });

  test('rejects invalid limits before reading', async () => {
    let calls = 0;
    const read = async () => { calls++; return { documents: [], checkpoint: 0 }; };
    for (const value of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
      await expect(readBoundedChanges(read, undefined, value)).rejects.toThrow('requestedLimit');
      for (const name of ['maxDocuments', 'targetBytes', 'maxDocumentBytes', 'readBlockDocuments']) {
        expect(() => validateBoundedReadOptions({ [name]: value })).toThrow(name);
      }
    }
    expect(() => validateBoundedReadOptions({ targetBytes: undefined })).toThrow('targetBytes');
    expect(calls).toBe(0);
  });
});
