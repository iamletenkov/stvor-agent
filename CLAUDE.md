@AGENTS.md

## Claude Code
- **Субагенты** (`.claude/agents/`, все — Opus 5.5, `model: opus`): `explorer` (чтение кода и решений) → `planner` (план) → `skeptic` (≤ 3 риска) → реализация → `reviewer` (вердикт перед PR); `test-runner` — прогон `make`-таргетов и диагноз падений. Большие чтения и длинные прогоны отдавай им — главный контекст дорогой.
- **Skills** (`.claude/skills/`): `/plan`, `/skeptic`, `/fullcheck` (`make ci` с логом в `.scratchpad/test-runs/`), `/release` (как выходит версия). Плагины: `superpowers:*` (brainstorming → writing-plans → subagent-driven-development → verification-before-completion), `/code-review`, `/commit-commands:commit`.
- **Правила** (`.claude/rules/`): `go.md` (Go-код агента), `shell.md` (скрипты пакета, `tools/` и `test/integration/`), `docs.md` (где что лежит).
- **Хуки** (`.claude/settings.json`) — enforcement: деструктивные команды и `--no-verify` блокируются; `Read` > 2 МБ без `limit` блокируется; после правки — shellcheck для shell и `gofmt` в контейнере для `.go`; на старте — git-контекст и `docs/STATUS.md`.
- **MCP** (`.mcp.json` из `.mcp.json.example`, ключи из env): `codebase-memory` — навигация по коду в первую очередь (slug даёт SessionStart-хук); `knowledge-graph` — память между сессиями (решения, причины багов, поправки владельца; не тривиальное); `context7` — документация библиотек.
- **Модель:** основная сессия — Opus 5.5 (`high`, рутина — `medium`); архитектура и спеки — `xhigh` в plan mode. Модель выбирается в начале сессии.
- **Сессии:** `/clear` между несвязанными задачами; перед концом длинной сессии — обновить `docs/STATUS.md`. Параллельные исполнители — только в своих worktree с непересекающимися файлами.
