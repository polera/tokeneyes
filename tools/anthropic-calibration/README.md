# Anthropic calibration tooling

This developer-only command builds reproducible candidate estimators from an
explicitly supplied public or synthetic corpus. Normal TokenEyes builds and
tests do not use an API key or access the network.

The workflow deliberately separates local preparation from content upload:

```sh
go run ./tools/anthropic-calibration split \
  --manifest corpus/manifest.json --out work/split-manifest.json \
  --seed tokeneyes-anthropic-v1

go run ./tools/anthropic-calibration features \
  --manifest work/split-manifest.json --out work/features.json

# This is the only command that sends corpus content anywhere. The consent
# flag is mandatory, as is a pinned model ID.
ANTHROPIC_API_KEY=... go run ./tools/anthropic-calibration label \
  --manifest work/split-manifest.json --out private/legacy-labels.json \
  --model PINNED_MODEL_ID --consent-send-to-anthropic

go run ./tools/anthropic-calibration fit \
  --labels private/legacy-labels.json --family legacy \
  --artifact-version claude-calibrated-legacy-v1 \
  --out work/legacy-candidate.json

go run ./tools/anthropic-calibration evaluate \
  --artifact work/legacy-candidate.json --labels private/legacy-labels.json \
  --json-out work/legacy-validation.json \
  --markdown-out work/legacy-validation.md

# Use the blind test once, for a release decision.
go run ./tools/anthropic-calibration evaluate \
  --artifact work/legacy-candidate.json --labels private/legacy-labels.json \
  --include-blind-test --json-out work/legacy-blind.json \
  --markdown-out work/legacy-blind.md

go run ./tools/anthropic-calibration verify-artifact \
  --artifact work/legacy-candidate.json --labels private/legacy-labels.json
```

Run the labeling and fitting sequence independently for the `legacy` and `new`
families. The labeler measures the fixed minimal-request baseline on every run,
resumes by stable sample ID, honors rate limits, atomically replaces its result
file, and stores no API key or source content. Its output contains the sample
digest, feature vector, pinned model, API version, request-shape version,
collection time, raw and baseline-adjusted counts, and retry history.

## Manifest

The manifest is JSON with a stable version and a `samples` array:

```json
{
  "version": "corpus-v1",
  "samples": [
    {
      "id": "synthetic-go-0001",
      "path": "content/go/0001.go",
      "license": "CC0-1.0",
      "provenance": "synthetic:generator-v1 seed=314159",
      "kind": "go",
      "language": "en",
      "length_bucket": "short",
      "group": "synthetic-go-template-1",
      "track": "text-component"
    },
    {
      "id": "synthetic-tools-0001",
      "content": "Call the weather tool for Paris.",
      "license": "CC0-1.0",
      "provenance": "synthetic:generator-v1 seed=314159",
      "kind": "tool-request",
      "language": "en",
      "length_bucket": "short",
      "group": "synthetic-tool-template-1",
      "track": "structured-request",
      "system": "Use tools when needed.",
      "tools": [{"name": "weather", "input_schema": {"type": "object"}}]
    }
  ]
}
```

Exactly one of `path` or inline `content` is required. Paths are relative to the
manifest. A text-component sample cannot contain system or tool blocks. Every
record requires explicit license/provenance, kind, length bucket, and split
group. The splitter hashes the seed and group, so all documents, repositories,
chunks, and generated-template siblings stay in one split.

Corpus payloads and provider labels are intentionally not bundled here. Before
calibration, construct at least 1,000 samples across every stratum in
`plan_anthropic_tokenizer.md`, confirm redistribution rights, and keep private
label files outside version control unless they contain only approved metadata.

## Promotion safety

`fit` emits `status: "candidate"`. Candidate artifacts can be evaluated but
the production predictor rejects them. An artifact marked `accepted` is also
rejected unless its stored blind-test metrics meet the family MAPE/p95, bias,
coverage, above-high, and 20% improvement gates. Promotion still requires the
release evidence and review described in the plan; the tool never changes the
production catalog.
