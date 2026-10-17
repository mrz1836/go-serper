# Synthetic SDK fixtures

Every file under this directory is an **SDK fixture**: written by hand for go-serper's tests,
shaped after the members the SDK types, with every value made up. None is a copy of a Serper
response, example or other site material, and none came from a live call.

- Hosts are `example.com`, `example.org` and `example.net` only.
- Queries, titles, snippets and page text are invented.
- Members the SDK does not type (such as `answerBox` or `topStories`) appear only to prove that
  they are ignored, and hold invented values too.

Never replace a fixture with a real response, and never commit one.

## Folders

One folder per operation, each holding only successful HTTP 200 responses:

- `search/`, Search responses:
  - `full.json`: a typical response with every list filled in, and untyped members.
  - `every-member.json`: every member the SDK types, plus unknown members at every level.
  - `empty.json`: no results.
  - `null-members.json` and `empty-members.json`: `null` lists and objects, then empty ones.
  - `escapes.json`: JSON escapes, a surrogate pair, a lone surrogate, NUL, U+2028 and U+2029.
  - `positions.json`: positions out of order, repeated, zero, negative, very large and missing, and
    dates in several formats.
- `images/`, Images responses:
  - `full.json`: a typical response.
  - `empty.json`: no results.
  - `kept-apart.json`: every `searchParameters` member, and `imageUrl`, `link` and `source` with values
    that differ, so that none can be filled in from another.
  - `sparse.json`: results that each hold only some members, one of them `{}`.
  - `null-images.json`: `null` images, `autocorrect` and `credits`.
- `scrape/`, Scrape responses:
  - `text.json` and `markdown.json`: text alone, then text and Markdown.
  - `empty-text.json`: `""` text, which is a success.
  - `markdown-null.json` and `markdown-empty.json`: a `null` Markdown, then an empty one.
  - `escapes.json`: JSON escapes, a surrogate pair, a lone surrogate, NUL, U+2028 and U+2029.
  - `pretty.json`: indented, with the members in another order.

## How the tests use them

The tests read a fixture with `fixture(tb, "<folder>/<file>")` in
[`helpers_test.go`](../../helpers_test.go). `TestDecodeFixtures` (via `srdRunFixtures` in
[`decode_harness_test.go`](../../decode_harness_test.go)) loads **every** `*.json` in each folder, so a new
file is tested as soon as it is added. Each one must decode the same live and offline, as plain
`encoding/json` decodes it, and round-trip unchanged. The respelled-names tests (`srdRunRenamed`) rewrite
every typed member name, wherever it appears, in every file of the folder. An attributes object must
therefore never use a member name the SDK types, such as `title`, as an attribute name. The decoder fuzz
targets, the memory tests and the decode tests also name some of these files directly.
