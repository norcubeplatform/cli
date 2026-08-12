# vhs tape for demo/demo.gif — run `vhs demo/demo.tape` from the repo root.
# Uses the fake norcube in demo/bin (sanitized canned output), not the real
# CLI, so no real org data lands in the recording. A tour of the whole CLI:
# session context, Backup, and Langsync.

Output demo/demo.gif

Set FontSize 16
Set Width 1120
Set Height 480
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms

Hide
Type "export PATH=demo/bin:$PATH && clear"
Enter
Show

Type "norcube whoami"
Sleep 400ms
Enter
Sleep 2s

Type "norcube backup health"
Sleep 400ms
Enter
Sleep 2.5s

Type "norcube backup download b81c22d0-56fa-4c19-9e01-3f8a2c47d1aa -d prod-postgres"
Sleep 400ms
Enter
Sleep 2.8s

Type "clear"
Sleep 300ms
Enter

Type "norcube langsync sync"
Sleep 400ms
Enter
Sleep 6.5s
