export interface BoundedReadOptions {
  maxDocuments?: number;
  targetBytes?: number;
  maxDocumentBytes?: number;
  readBlockDocuments?: number;
}

const defaults: Required<BoundedReadOptions> = {
  maxDocuments: 50,
  targetBytes: 8 * 1024 * 1024,
  maxDocumentBytes: 16 * 1024 * 1024,
  readBlockDocuments: 4,
};

export const validateBoundedReadOptions = (
  options: BoundedReadOptions = {},
): Required<BoundedReadOptions> => {
  const limits = { ...defaults, ...options };
  for (const [name, value] of Object.entries(limits)) {
    if (!Number.isSafeInteger(value) || value <= 0) {
      throw new RangeError(`${name} must be a positive safe integer`);
    }
  }
  return limits;
};

const encoder = new TextEncoder();

export const readBoundedChanges = async <T, C>(
  read: (
    limit: number,
    checkpoint: C | undefined,
  ) => Promise<{ documents: T[]; checkpoint: C }>,
  checkpoint: C | undefined,
  requestedLimit: number,
  options: BoundedReadOptions = {},
): Promise<{ documents: T[]; checkpoint: C | undefined }> => {
  if (!Number.isSafeInteger(requestedLimit) || requestedLimit <= 0) {
    throw new RangeError('requestedLimit must be a positive safe integer');
  }
  const limits = validateBoundedReadOptions(options);
  const count = Math.min(requestedLimit, limits.maxDocuments);
  const documents: T[] = [];
  let cursor = checkpoint;
  let pageBytes = 0;

  while (true) {
    let limit = Math.min(limits.readBlockDocuments, count - documents.length);
    while (true) {
      const chunk = await read(limit, cursor);
      if (chunk.documents.length > limit) {
        throw new RangeError('Changed-document read exceeded its requested limit');
      }
      const sizes = chunk.documents.map((document) => {
        const json = JSON.stringify(document);
        if (json === undefined) {
          throw new TypeError('Changed document must be JSON serializable');
        }
        const size = encoder.encode(json).byteLength;
        if (size > limits.maxDocumentBytes) {
          throw new RangeError('Changed document exceeds its encoded byte limit');
        }
        return size;
      });
      if (sizes.length === 0) return { documents, checkpoint: cursor };

      let prefix = 0;
      let admittedBytes = 0;
      for (const size of sizes) {
        if (pageBytes + admittedBytes + size > limits.targetBytes) break;
        admittedBytes += size;
        prefix++;
      }
      // A legal row larger than the page target must still make progress.
      if (documents.length === 0 && prefix === 0) {
        prefix = 1;
        admittedBytes = sizes[0];
      }
      if (prefix === 0) return { documents, checkpoint: cursor };
      if (prefix < chunk.documents.length) {
        // Opaque checkpoints cannot be derived from a truncated page. Reread
        // from the unchanged cursor and account for concurrent row changes.
        limit = prefix;
        continue;
      }

      documents.push(...chunk.documents);
      pageBytes += admittedBytes;
      cursor = chunk.checkpoint;
      if (pageBytes >= limits.targetBytes || documents.length >= count) {
        return { documents, checkpoint: cursor };
      }
      break;
    }
  }
};
