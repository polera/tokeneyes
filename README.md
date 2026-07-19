# TokenEyes

[![Build binaries](https://github.com/polera/tokeneyes/actions/workflows/binaries.yml/badge.svg)](https://github.com/polera/tokeneyes/actions/workflows/binaries.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

TokenEyes is an offline-first Go CLI for estimating mixed text, image, audio, and document usage across OpenAI/Codex, Claude, and Gemini models. It reports per-modality formulas, bounded estimates, capability status, request planning, context fit, fixed-point API cost scenarios, and privacy-safe local history.

Repository content stays local unless `--verify` is explicitly supplied. Saved runs contain source labels/paths, SHA-256 hashes, byte counts, token results, and configuration, never source or prompt contents.

## Install

Tagged [GitHub releases](https://github.com/polera/tokeneyes/releases) include archives for Linux, macOS, and Windows on AMD64 and ARM64, plus a `checksums.txt` file containing SHA-256 checksums. Extract the archive for your platform and place `tokeneyes` (or `tokeneyes.exe` on Windows) on your `PATH`.

To install with Go, use Go 1.26 or newer:

```sh
go install github.com/polera/tokeneyes/cmd/tokeneyes@latest
```

From a checkout:

```sh
go build -o tokeneyes ./cmd/tokeneyes
```

## Examples

```sh
# Estimate a prompt and selected files with exact local OpenAI BPE counting.
tokeneyes estimate README.md 'pkg/**/*.go' --prompt 'Review this code' --model gpt-5.5

# Compare the same tracked repository payload across provider families.
tokeneyes compare --preset tracked \
  --models gpt-5.5,claude-sonnet-4-6,gemini-3.5-flash --tui

# Read a prompt from stdin and emit stable JSON without saving it.
printf 'Explain this patch' | tokeneyes estimate --stdin --preset changed --json --no-save

# Include explicit overhead and response assumptions.
tokeneyes compare . --system-file system.txt --tools-file tools.json \
  --profile codex --output-tokens 1000,4000,16000 --reasoning-tokens 8000

# Explicitly send the assembled request to official counting endpoints.
ANTHROPIC_API_KEY=... tokeneyes estimate plan.md --model claude --verify
GEMINI_API_KEY=... tokeneyes estimate plan.md --model gemini --verify

# Inspect and compare privacy-safe saved runs.
tokeneyes history
tokeneyes diff 20260716T120000Z-a1b2c3d4 20260716T130000Z-e5f6a7b8
tokeneyes models list
tokeneyes models show gpt-5.5

# Estimate a mixed native request. Unsupported modalities remain visible.
tokeneyes compare prompt.md screenshot.png meeting.wav report.pdf \
  --models gpt-5.6,claude-opus-4-8,claude-sonnet-5,gemini-3.5-flash \
  --processing native --image-detail high --document-detail auto

# Count an audio transcript without uploading or transcribing the recording.
tokeneyes estimate meeting.mp3 --model claude-sonnet-5 \
  --processing normalized-text --transcript meeting.mp3=meeting.txt
```

`--verify` never generates a model response. Claude uses the [message token-counting endpoint](https://platform.claude.com/docs/en/api/messages/count_tokens), Gemini uses [`countTokens`](https://ai.google.dev/api/tokens), and unsupported models return a labeled warning or fail closed with `--require-verification`.

## Inputs and safety

The collector accepts text/code plus PNG, JPEG, WebP, GIF (first frame), WAV, MP3, AAC/M4A, FLAC, Ogg, PDF, DOCX, PPTX, and XLSX. Format detection uses content signatures rather than trusting extensions. It accepts positional files, directories, and globs plus:

- `--prompt`, `--prompt-file`, or `--stdin` (`-` is an alias for stdin)
- `--preset tracked`, `--preset changed`, or `--preset plan`
- per-file and total limits via `--max-file-bytes` and `--max-total-bytes`
- media limits via `--max-media-size`, `--max-media-count`, `--max-pages`, and `--max-duration`
- `--processing native|normalized-text`, `--image-detail`, and `--document-detail`
- repeatable transcript links via `--transcript audio-path=text-path`

Directory scans apply `.gitignore` and `.tokeneyesignore`, skip common dependency/build directories, generated files, and unrecognized binary formats, and do not follow symlinks. Ordering is deterministic. Unreadable files and exceeded limits mark a scan incomplete and produce warnings.

## Configuration

Put defaults in `.tokeneyes.yaml`; flags take precedence:

```yaml
model: gpt-5.5
models: [gpt-5.5, claude-sonnet-4-6, gemini-3.5-flash]
output_tokens: [1000, 4000, 16000]
reasoning_tokens: 0
cached_tokens: 0
profile: none
max_file_bytes: 5242880
max_total_bytes: 104857600
max_media_size: 52428800
max_media_count: 100
max_pages: 600
max_duration: 4h
processing: native
image_detail: auto
document_detail: auto
transcripts: []
overrides:
  - glob: "archive/**/*.pdf"
    processing: normalized-text
    document_detail: text
workers: 4
no_save: false
fail_incomplete: false
fail_overflow: false
max_input_tokens: 0
max_cost_usd: ""
```

Ordered override rules are applied to matching source paths; later matches replace fields set by earlier matches. Use `--config path/to/config.yaml` to select another file. Credentials are read only from `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, or `GOOGLE_API_KEY` and are not stored. File upload permission is intentionally CLI-only: `--allow-file-upload` requires `--verify` and cannot be inherited from repository configuration.

The embedded catalog is an immutable release snapshot. `--catalog override.json` replaces matching model entries and adds new entries; `tokeneyes models show MODEL --json` prints the required JSON shape. Every result includes its catalog version and pricing date, and data older than 180 days or past an explicit pricing validity window is warned as stale. Costs represent public API list-price scenarios, not subscription usage or invoice reconciliation.

The bundled `2026-07-19` catalog contains:

| Provider | Models |
| --- | --- |
| OpenAI | `gpt-5.6`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.4-nano` |
| Anthropic | `claude-fable-5`, `claude-opus-4-8`, `claude-opus-4-7`, `claude-sonnet-5`, `claude-sonnet-4-6`, `claude-haiku-4-5` |
| Google | `gemini-3.1-pro-preview`, `gemini-3.5-flash`, `gemini-3.1-flash-lite`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite` |

OpenAI long-context pricing starts above 272,000 input tokens for the applicable GPT-5.x models. Gemini Pro long-context pricing starts above 200,000 input tokens, and Gemini 2.5 Flash models apply their audio-specific input prices when estimating audio. Use `tokeneyes models list` and `tokeneyes models show MODEL` to inspect the catalog shipped with your installed version.

## Output and CI

Human output is the default. Add `--tui` to `estimate` or `compare` for a compact terminal dashboard with per-model context bars and expected costs. It respects `NO_COLOR` and the `COLUMNS` environment variable; `--tui` and `--json` are mutually exclusive.

`--json` emits `tokeneyes.run.v2`, preserving phase-one fields and adding privacy-safe `assets`, `request_plan`, `count_components`, `capability_status`, and verification transport metadata. SQLite migration 2 keeps old v1 payloads readable. Source bytes, extracted document text, transcripts, thumbnails, and upload identifiers are never persisted.

Threshold flags have stable exit codes:

| Code | Meaning |
| ---: | --- |
| 0 | success |
| 2 | usage/configuration error |
| 3 | context overflow with `--fail-overflow` |
| 4 | `--max-input-tokens` exceeded |
| 5 | `--max-cost-usd` exceeded by the expected scenario |
| 6 | incomplete scan with `--fail-incomplete` |
| 7 | required verification failed |

## Development

```sh
go test ./...
go vet ./...
```

The reusable engine is in `pkg/tokeneyes`. Its collector, counter, verifier, and run store are behind interfaces, so applications can replace filesystem collection, tokenization, verification transport, or persistence independently of the CLI.

## License

TokenEyes is available under the [MIT License](LICENSE).
