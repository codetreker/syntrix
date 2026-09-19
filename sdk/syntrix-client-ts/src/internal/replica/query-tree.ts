type Node<K, V> = { key: K; value: V; left: Node<K, V> | null; right: Node<K, V> | null; height: number };
const height = <K, V>(node: Node<K, V> | null): number => node?.height ?? 0;
const update = <K, V>(node: Node<K, V>): Node<K, V> => {
  node.height = 1 + Math.max(height(node.left), height(node.right));
  return node;
};
const rotateLeft = <K, V>(node: Node<K, V>): Node<K, V> => {
  const next = node.right!;
  node.right = next.left;
  next.left = update(node);
  return update(next);
};
const rotateRight = <K, V>(node: Node<K, V>): Node<K, V> => {
  const next = node.left!;
  node.left = next.right;
  next.right = update(node);
  return update(next);
};
const balance = <K, V>(node: Node<K, V>): Node<K, V> => {
  update(node);
  const difference = height(node.left) - height(node.right);
  if (difference > 1) {
    if (height(node.left!.left) < height(node.left!.right)) node.left = rotateLeft(node.left!);
    return rotateRight(node);
  }
  if (difference < -1) {
    if (height(node.right!.right) < height(node.right!.left)) node.right = rotateRight(node.right!);
    return rotateLeft(node);
  }
  return node;
};

// Values are references owned by the query evaluator. The tree retains no
// document payload and allocates one node per distinct comparator key.
export class OrderedQueryTree<K, V> {
  private root: Node<K, V> | null = null;
  private count = 0;
  constructor(private readonly compare: (left: K, right: K) => number) {}
  get size(): number { return this.count; }
  get height(): number { return height(this.root); }
  get(key: K): V | undefined {
    let node = this.root;
    while (node) {
      const order = this.compare(key, node.key);
      if (order === 0) return node.value;
      node = order < 0 ? node.left : node.right;
    }
    return undefined;
  }
  insert(key: K, value: V): void {
    const visit = (node: Node<K, V> | null): Node<K, V> => {
      if (!node) { this.count++; return { key, value, left: null, right: null, height: 1 }; }
      const order = this.compare(key, node.key);
      if (order === 0) { node.key = key; node.value = value; return node; }
      if (order < 0) node.left = visit(node.left);
      else node.right = visit(node.right);
      return balance(node);
    };
    this.root = visit(this.root);
  }
  remove(key: K): boolean {
    let found = false;
    const visit = (node: Node<K, V> | null, target: K): Node<K, V> | null => {
      if (!node) return null;
      const order = this.compare(target, node.key);
      if (order < 0) node.left = visit(node.left, target);
      else if (order > 0) node.right = visit(node.right, target);
      else {
        if (!found) { found = true; this.count--; }
        if (!node.left) return node.right;
        if (!node.right) return node.left;
        let successor = node.right;
        while (successor.left) successor = successor.left;
        node.key = successor.key;
        node.value = successor.value;
        node.right = visit(node.right, successor.key);
      }
      return balance(node);
    };
    this.root = visit(this.root, key);
    return found;
  }
  *entries(after?: K): IterableIterator<readonly [K, V]> {
    const stack: Node<K, V>[] = [];
    let node = this.root;
    while (node || stack.length) {
      while (node) {
        if (after !== undefined && this.compare(node.key, after) <= 0) node = node.right;
        else { stack.push(node); node = node.left; }
      }
      if (!stack.length) break;
      const next = stack.pop()!;
      yield [next.key, next.value];
      node = next.right;
    }
  }
  clear(): void { this.root = null; this.count = 0; }
}
