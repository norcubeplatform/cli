# vhs tape for demo/demo.gif — run `vhs demo/demo.tape` from the repo root.
# Uses the fake norcube in demo/bin (sanitized canned output), not the real
# CLI, so no real org data lands in the recording. A tour of the whole CLI:
# session context, Backup health, scripting with -o json --query, any API
# endpoint with `norcube api`, and the CLI sessions you can revoke.

Output demo/demo.gif

Set FontSize 16
Set Width 1340
Set Height 520
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 35ms

Hide
Type "export PATH=demo/bin:$PATH && clear"
Enter
Show

Type "norcube whoami"
Sleep 400ms
Enter
Sleep 1.8s

Type "norcube backup health"
Sleep 400ms
Enter
Sleep 2.5s

Type "clear"
Sleep 300ms
Enter

Type `norcube backup jobs list -o json --query "[?jobStatus=='failed'].{db: datasourceName, error: jobErrorSummary}"`
Sleep 500ms
Enter
Sleep 2.5s

Type `norcube api backup /datasources --query "list[].{database: name, lastBackup: lastBackupAt}"`
Sleep 500ms
Enter
Sleep 2.5s

Type "norcube auth sessions list"
Sleep 400ms
Enter
Sleep 3s
