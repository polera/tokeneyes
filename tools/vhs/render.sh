#!/bin/sh
# Regenerate the README screenshots in assets/ with VHS.
#
# Requires: vhs (https://github.com/charmbracelet/vhs), python3, go.
# The command runs only against generated source data and never reads local
# TokenEyes history or repository contents.
#
# Usage:  tools/vhs/render.sh          # render all tapes
#         tools/vhs/render.sh compare  # render one tape by name
set -eu

cd "$(dirname "$0")/../.."
ROOT=$(pwd)
DEMO="$ROOT/tools/vhs/.demo"

command -v vhs >/dev/null 2>&1 || {
	echo "vhs is required: https://github.com/charmbracelet/vhs" >&2
	exit 1
}

mkdir -p "$ROOT/assets" "$DEMO"
go build -o "$DEMO/tokeneyes" ./cmd/tokeneyes

# Build a deterministic, repository-sized Go source fixture. Keeping it in the
# ignored demo directory makes the screenshots reproducible without checking a
# large throwaway input into the repository.
python3 - "$DEMO/sample.go" <<'PY'
import sys

path = sys.argv[1]
with open(path, "w", encoding="utf-8") as fixture:
    fixture.write("package checkout\n\nimport \"fmt\"\n\ntype Order struct { CustomerID string; TotalCents int64 }\n\n")
    for number in range(8000):
        fixture.write(
            f"func validateOrder{number:04d}(order Order) error {{\n"
            "\tif order.CustomerID == \"\" { return fmt.Errorf(\"customer is required\") }\n"
            "\tif order.TotalCents < 0 { return fmt.Errorf(\"total must not be negative\") }\n"
            "\treturn nil\n"
            "}\n\n"
        )
PY

cat > "$DEMO/run.sh" <<EOF
#!/bin/sh
set -eu
cd "$ROOT"
unset NO_COLOR
printf '\033[?25l\033[2J\033[H'

case "\${1:-}" in
compare)
	COLUMNS=120 "$DEMO/tokeneyes" compare --prompt-file "$DEMO/sample.go" \\
		--models gpt-5.5,claude-sonnet-4-6,gemini-3.5-flash \\
		--profile codex --output-tokens 1000,4000,16000 --tui --no-save
	;;
compact)
	COLUMNS=52 "$DEMO/tokeneyes" estimate --prompt-file "$DEMO/sample.go" \\
		--model gpt-5.5 --profile codex --output-tokens 4000 --tui --no-save
	;;
*)
	echo "usage: run.sh compare|compact" >&2
	exit 2
	;;
esac

# Keep the shell busy so the capture contains output, not a trailing prompt.
sleep 10
EOF
chmod +x "$DEMO/run.sh"

tapes=${*:-"compare compact"}
for tape in $tapes; do
	case "$tape" in
	compare|compact) ;;
	*)
		echo "unknown tape: $tape" >&2
		exit 2
		;;
	esac
	echo "rendering $tape..."
	vhs "tools/vhs/$tape.tape"
done

rm -f tools/vhs/.render.gif
echo "done -> assets/"
