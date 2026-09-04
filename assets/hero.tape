# hero.gif — the 30-second agentic pitch: same claude, what it cost, on any model.
# Record from the repo root with:  vhs assets/hero.tape

Output assets/hero.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 675
Set Padding 20
Set TypingSpeed 55ms

# Quiet prompt, no shell-init scrollback in frame one.
# A recorded session must not inherit this session's Claude Code env, or the
# child launches as a sub-session and the splash fills with inherited warnings.
Hide
Type "unset CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_EXECPATH CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_CODE_SESSION_ID CLAUDE_CODE_VERSION CLAUDE_EFFORT CLAUDE_PID CLAUDECODE CLAUDER_TELEGRAM_TOKEN"
Enter
Type "export PS1='❯ ' && clear"
Enter
Show

# 1. It is the claude you already know.
Type "agentic"
Enter
Sleep 8s
Type "/exit"
Enter
Sleep 3s
Hide
Type "clear"
Enter
Show

# 2. ...that now tells you what today cost.
Type "agentic cost --by model"
Enter
Sleep 6s
Hide
Type "clear"
Enter
Show

# 3. ...on any model you point it at.
Type "agentic models list"
Enter
Sleep 5s
