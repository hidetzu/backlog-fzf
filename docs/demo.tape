# VHS tape for the README demo. Regenerate with `make demo`.
# Uses the fictional data from scripts/demo-seed (no API key / network).

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
Type "export PS1='$ ' XDG_CONFIG_HOME=$PWD/tmp/demo/config XDG_DATA_HOME=$PWD/tmp/demo/data FZF_DEFAULT_OPTS='--tabstop=2' && clear"
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
Screenshot docs/screenshot.png

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
