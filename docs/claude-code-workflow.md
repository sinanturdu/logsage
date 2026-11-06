# Working with Claude Code

## Setup
```bash
curl -fsSL https://claude.ai/install.sh | bash   # macOS, Linux, WSL
claude --version
cd logsage && claude                              # first run asks you to log in
```
Windows PowerShell: `irm https://claude.ai/install.ps1 | iex`

Claude Code reads `CLAUDE.md` (which imports `AGENTS.md`) at the start of every session, so project rules are always loaded.

## Loop for every task
1. Start from a clean branch: `git switch -c feat/<task>`
2. Ask for a plan first. Approve or correct it.
3. Let it implement in small steps, run tests, and show the diff.
4. Read the diff yourself. If you cannot explain it, ask.
5. Commit with a conventional message, push, open a PR, wait for CI, merge.
6. Tick the roadmap checkbox in the same PR.

## Example first prompts
- "Read AGENTS.md and docs/. Plan v0.1 tasks 1-3 (module, layout, healthz). Do not write code until I approve."
- "Implement the approved plan. Run go vet and go test. Show me the diff."
- "Commit this on the current branch with a conventional commit message. Do not push."

## GitHub
Use the GitHub CLI for authentication (`gh auth login`); Claude Code runs `git` and `gh` as you, with your approval for each command.
