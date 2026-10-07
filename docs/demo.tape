# VHS tape for the README demo. Regenerate with `make demo`.
# Uses the fictional data from scripts/demo-seed (no API key / network).
# BKFZ_DEMO_DIR is exported by `make demo` and must be the seeded directory.

Output docs/demo.gif

Require bkfz
Require fzf

Set Shell "bash"
Set FontSize 15
Set Width 1800
Set Height 680
Set Padding 16
Set TypingSpeed 120ms
Set Theme "Catppuccin Mocha"

Hide
# Exit the shell (recording nothing) unless BKFZ_DEMO_DIR is set, so a
# misconfigured run can never fall back to the real ~/.config/bkfz.
Type `[ -n "${BKFZ_DEMO_DIR:-}" ] || exit 1; export PS1='$ ' XDG_CONFIG_HOME="$BKFZ_DEMO_DIR/config" XDG_DATA_HOME="$BKFZ_DEMO_DIR/data" FZF_DEFAULT_OPTS='--tabstop=2' && clear`
Enter
Show

Type "bkfz"
Sleep 400ms
Enter
Sleep 1.5s

# 2-character Japanese query matches issues and documents alike
Type "認証"
Sleep 2s
Down
Sleep 1.2s
Down
Sleep 1.2s
Down
Sleep 1.5s

# Narrow down further with a second word
Type " ログイン"
Sleep 2.5s

# Start over with an English query
Ctrl+U
Sleep 500ms
Type "rate limit"
Sleep 2.5s

Escape
Sleep 500ms
