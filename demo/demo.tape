# vhs tape for demo/demo.gif — run `vhs demo/demo.tape` from the repo root.
# Uses the fake norcube in demo/bin (sanitized canned output), not the real
# CLI, so no real org data lands in the recording.

Output demo/demo.gif

Set FontSize 16
Set Width 1000
Set Height 480
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms

Hide
Type "export PATH=demo/bin:$PATH && clear"
Enter
Show

Type "norcube backup health"
Sleep 500ms
Enter
Sleep 2.5s

Type "norcube backup list --limit 3"
Sleep 500ms
Enter
Sleep 2.5s

Type "norcube backup download b81c22d0-56fa-4c19-9e01-3f8a2c47d1aa -d prod-postgres"
Sleep 500ms
Enter
Sleep 3s

Type "norcube backup restore-test run b81c22d0-56fa-4c19-9e01-3f8a2c47d1aa -d prod-postgres"
Sleep 500ms
Enter
Sleep 3.5s
