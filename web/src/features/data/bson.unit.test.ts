import assert from "node:assert/strict";
import { test } from "node:test";
import { bsonDetail, bsonText } from "./bson.ts";

test("cells read as the shell shows values", () => {
  assert.equal(bsonText('"open"'), "open");
  assert.equal(bsonText('{"$numberInt":"2"}'), "2");
  assert.equal(bsonText('{"$numberDecimal":"249.00"}'), "249.00");
  assert.equal(
    bsonText('{"$oid":"6745f0c2e1b7a3d4c5f2a91e"}'),
    "6745f0c2e1b7a3d4c5f2a91e",
  );
  assert.equal(
    bsonText('{"$date":"2026-11-27T09:14:02.000Z"}'),
    "2026-11-27 09:14:02",
  );
  assert.equal(
    bsonText('{"email":{"$masked":true},"name":"Ada"}'),
    "{ email, name }",
  );
  assert.equal(bsonText("[1,2,3]"), "[3]");
  assert.equal(bsonText("null"), "null");
  assert.equal(
    bsonText(
      '{"$binary":{"base64":"MDEyMzQ1Njc4OWFiY2RlZg==","subType":"04"}}',
    ),
    "30313233-3435-3637-3839-616263646566",
  );
});

test("the drawer writes the whole value, unwrapped", () => {
  assert.equal(
    bsonDetail(
      '{"a":{"$numberInt":"1"},"at":{"$date":"2026-11-27T09:14:02.000Z"},"e":{"$masked":true},"l":[]}',
    ),
    '{\n  "a": 1,\n  "at": ISODate("2026-11-27T09:14:02.000Z"),\n  "e": ••••••,\n  "l": []\n}',
  );
});
