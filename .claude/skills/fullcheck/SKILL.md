---
name: fullcheck
description: Полная проверка перед коммитом/PR — make ci (линт, тесты, сборка) с полным логом в .scratchpad/test-runs/ и коротким вердиктом.
allowed-tools: Bash(make ci), Bash(mkdir -p .scratchpad/test-runs), Bash(git status*), Bash(git diff*), Read, Grep, Glob
---
# /fullcheck

1. `mkdir -p .scratchpad/test-runs`, затем `make ci > .scratchpad/test-runs/<дата-время>-ci.log 2>&1; echo exit=$?` — один прогон за раз (хост общий). Длинный разбор можно отдать агенту `test-runner`, если он есть в репозитории.
2. Красное: читай лог узко (`Grep -n` → `Read` с `offset/limit`), чини причину. **Тесты под код не подгоняй.** Ошибка docker/сети — один повтор.
3. Итог: что прогнано, exit-код, путь к логу; вердикт READY / NEEDS FIXES.

«Зелёное» утверждай только по `exit=0` реального прогона.
