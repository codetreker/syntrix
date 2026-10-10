---
name: scope-cut
description: Use when the user requests a scope decision, a focused split of an oversized change, or evaluation of a proposed deferral. Records deferral costs and constraints that preserve future options.
---

# Cutting Scope

A deferral is a decision about future cost. Apply this skill to the requested scope discussion; it does not authorize removing requested functionality or imposing additional cuts. Read [agent-notes](../agent-notes/SKILL.md) when recording the decision.

For each candidate ask: **Does deferring this mean adding it later, or redoing work later?**

| Kind | Cost of deferral | Decision to evaluate |
|---|---|---|
| Feature | Add behavior later | Consider deferral only when compatible with the confirmed scope. |
| Guarantee | Rearrange existing components later | Deliver it now, or explicitly agree on the resulting rewrite and missing guarantee. |
| Shape constraint | Cheap only if the current design leaves room | Preserve the required form without claiming the future behavior exists. |

A scope decision needs three outputs: what is included, what is deferred with its cost, and which deferred capabilities impose constraints on the current design. Do not let an invisible future rewrite masquerade as a free simplification.

## Distinguish the costs

A feature adds behavior at a call site. A guarantee depends on how components are arranged and what they promise each other. Probe whether the candidate:

- constrains every component, as ordering, atomicity, or error propagation may do;
- would require changes to otherwise unrelated call sites;
- changes default behavior that would need auditing across existing paths;
- is already assumed by downstream consumers.

Several positive answers indicate structural cost; investigate the actual dependencies before calling the deferral cheap. Importance or line count alone does not determine the classification.

A guarantee must hold in the delivered system. A shape constraint only keeps a future capability possible. Phrase the latter as a restriction on implementation form, such as “Do not choose a write sequence that prevents an atomic rename.” This does not claim that rename is currently atomic. If no concrete restriction can be stated, reconsider whether the candidate is actually a guarantee.

## Record a defensible scope decision

1. Derive candidates from the user's confirmed scope and existing requirements in `docs/design/`, `docs/reference/`, local `docs/plans/`, and README documents. Use existing requirement IDs or links where available. Resolve material omissions in the owning document; do not invent requirements or require a new specification tree.
2. Classify each candidate and identify what would have to change if it were deferred. State the concrete cost, not a label such as “low.”
3. Turn every condition that keeps a deferral cheap into a constraint on the current design. Preserve existing constraints unless the scope decision explicitly changes them.
4. Identify the observable outcome when a deferred case is encountered: an error, a limit, a documented slow path, or another precise behavior. “Not well supported” is neither implementable nor testable.
5. Record the decision and its costs in an Agent Note, synchronizing affected active requirements, design documents, and the local execution plan. Keep concise Why and How in the design document and link to the full rationale.
6. In `## Alternatives`, record only other scope choices actually considered and what they would have bought or cost. If none were considered, say so. Do not fabricate a smaller or larger choice to fill the section.

Honor decisions already confirmed by the user. If the requested functionality would be removed, make that proposed change explicit and obtain the required scope decision before implementing it. Work already within the confirmed scope can continue.

## Failure patterns

**Deferring a small guarantee.** A version check may be one line, but adding it after the write path is built can require a redesign. Judge where it belongs, not its size.

**Deleting a shape constraint under simplification.** State which future option becomes costly or impossible before removing a constraint.

**Citing a deferral nobody decided.** A document that does not authorize the cut cannot establish it. Record real scope decisions at their owner.

**Presenting sequencing as scope.** Moving work to a later task changes delivery order; it does not remove an accepted requirement. A real deferral needs an explicit outcome, cost, and owner.

**Listing only benefits.** State the missing behavior, operational limits, and rewrite exposure that the decision accepts.
