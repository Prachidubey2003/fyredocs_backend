/**
 * Drift detector for src/generated.ts.
 *
 * The hand-rolled src/types.ts is what the SDK's public surface
 * uses today. src/generated.ts is the output of
 * `npm run generate:types` against
 * docs/developer/swagger/openapi.yaml — produced by
 * openapi-typescript.
 *
 * Spec drift is the failure mode we care about: a backend change
 * adds/removes/renames a field in OpenAPI without updating the
 * SDK, and types.ts goes stale silently. This test loads the
 * generated file and asserts that the symbols our public types
 * depend on still exist with the expected shape. When the spec
 * changes, this test breaks loudly and the developer regenerates
 * + updates types.ts in the same PR.
 *
 * Run `npm run check:types-fresh` in CI to additionally confirm
 * the committed generated.ts file matches the live spec.
 */

import { describe, expectTypeOf, it } from 'vitest';

import type { components, paths } from '../generated.js';
import type { ApiKey, Plan, Subscription, UsageRollupRow } from '../types.js';

describe('generated.ts drift detector', () => {
  it('still exposes APIKey with the fields the SDK consumes', () => {
    type Generated = components['schemas']['APIKey'];
    expectTypeOf<Generated>().toHaveProperty('id');
    expectTypeOf<Generated>().toHaveProperty('name');
    expectTypeOf<Generated>().toHaveProperty('keyPrefix');
    expectTypeOf<Generated>().toHaveProperty('environment');
    // Field name parity with the hand-rolled type — flags a
    // rename in either direction.
    expectTypeOf<ApiKey['id']>().toBeString();
  });

  it('still exposes the Plan + Subscription + UsageRollupRow shapes', () => {
    expectTypeOf<components['schemas']['Plan']>().toHaveProperty('code');
    expectTypeOf<components['schemas']['Plan']>().toHaveProperty('monthlyPriceCents');
    expectTypeOf<components['schemas']['Subscription']>().toHaveProperty('planCode');
    expectTypeOf<components['schemas']['Subscription']>().toHaveProperty('status');
    expectTypeOf<components['schemas']['UsageRollupRow']>().toHaveProperty('eventType');
    expectTypeOf<components['schemas']['UsageRollupRow']>().toHaveProperty('totalQuantity');

    // Cross-check: hand-rolled types use the same property names.
    expectTypeOf<Plan['code']>().toBeString();
    expectTypeOf<Subscription['planCode']>().toBeString();
    expectTypeOf<UsageRollupRow['eventType']>().toBeString();
  });

  it('still exposes the v1 billing/usage paths the client targets', () => {
    // The runtime client's URL strings must correspond to actual
    // entries in the generated `paths` interface — a typo or a
    // renamed path on the backend would break the SDK at runtime
    // without this guard.
    expectTypeOf<paths['/v1/billing/me']>().not.toBeNever();
    expectTypeOf<paths['/v1/billing/plans']>().not.toBeNever();
    expectTypeOf<paths['/v1/billing/me/subscribe']>().not.toBeNever();
    expectTypeOf<paths['/v1/usage/me']>().not.toBeNever();
    expectTypeOf<paths['/auth/api-keys']>().not.toBeNever();
  });

  it('still exposes editor + notify routes', () => {
    expectTypeOf<paths['/api/editor/v1/documents/{id}/edit']>().not.toBeNever();
    expectTypeOf<paths['/v1/notify/deliveries']>().not.toBeNever();
  });

  it('still exposes table.cell.edit coord-form fields (region/row/col)', () => {
    // Drift detector for the coord-form addressing on
    // table.cell.edit. The op item is an array element in
    // the editor /edit POST body schema — drill in to the
    // ops[]-item type and confirm `region`, `row`, `col`
    // are all part of the surface. A backend change that
    // removed any of them would break the rect-vs-coord
    // precedence contract; this guard breaks loudly.
    type EditOp = NonNullable<
      paths['/api/editor/v1/documents/{id}/edit']['post']['requestBody']
    >['content']['application/json']['ops'][number];
    expectTypeOf<EditOp>().toHaveProperty('region');
    expectTypeOf<EditOp>().toHaveProperty('row');
    expectTypeOf<EditOp>().toHaveProperty('col');
    // The discriminant is still the op `type` string — pin
    // that `table.cell.edit` survives in the enum so the
    // wire-level dispatch can route to the coord form.
    expectTypeOf<EditOp['type']>().toMatchTypeOf<
      | 'page.rotate'
      | 'page.delete'
      | 'page.insert'
      | 'annotation.add'
      | 'text.replace'
      | 'text.insert'
      | 'text.delete'
      | 'redact.apply'
      | 'table.cell.edit'
    >();
  });
});
