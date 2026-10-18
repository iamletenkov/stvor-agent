---
name: plan
description: Составить план реализации — explorer собирает контекст, planner пишет план с Progress Tracker; результат в docs/plans/ и строкой в docs/README.md.
argument-hint: <задача или путь к спеке>
---
# /plan $ARGUMENTS

1. Агент **explorer** — код, конвенции, ADR и спеки по теме.
2. Агент **planner** — план по его формату: `## Progress Tracker` в шапке, тест первым в каждой задаче, последняя задача — «Финальная интеграция + `make ci`».
3. Сохрани в `docs/plans/YYYY-MM-DD-<slug>.md` (slug — как у спеки) и добавь строку в `docs/README.md`.
4. Нетривиальная задача — дальше `/skeptic docs/plans/<файл>`; риски вносятся в план до кода.

План ничего не реализует.
